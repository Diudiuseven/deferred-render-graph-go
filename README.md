# deferred-render-graph-go

基于渲染图（Render Graph）组织的延迟着色渲染管线（CPU 参考实现）。
渲染图负责解析多个渲染通道对中间结果的读写依赖、调度执行、管理中间结果的
生命周期与复用；延迟着色管线在其上实现几何阶段 + 分块光照阶段。

## 结构

```
rendergraph/   渲染图核心：依赖解析、调度、资源池、计划导出
deferred/      延迟着色管线：GBuffer、几何通道、（分块）光照通道
cmd/demo/      演示程序：渲染一帧并导出调度计划
```

## 渲染图的描述方式

渲染图由**资源**与**通道**两部分声明组成（`rendergraph` 包）：

```go
g := rendergraph.New()
// 1. 声明中间结果（名称、格式、尺寸）
g.DeclareResource(rendergraph.ResourceDesc{Name: "gbuffer.albedo", Format: rendergraph.FormatRGBA32F, Width: w, Height: h})
// 2. 添加通道：声明它读哪些资源、写哪些资源，以及执行体
g.AddPass(&rendergraph.Pass{
    Name:   "geometry",
    Writes: []rendergraph.ResourceID{"gbuffer.albedo"},
    Execute: func(ctx *rendergraph.ExecContext) error { /* 写 ctx.Get("gbuffer.albedo") */ },
    ConfigKey: "geometry-v1", // 配置标识，变更时用于增量重执行
})
```

依赖规则（编译期自动推导）：

- **RAW**：读者依赖该资源的每一个写者，写者先执行；
- **WAW**：同一资源的多个写者按声明顺序排序；
- **只读共享**不产生依赖，可并行；
- 相互形成依赖环时，`Compile` 返回 `*rendergraph.CycleError`，
  错误信息包含构成环的通道名路径（如 `p1 -> p2 -> p3 -> p1`），
  不会静默产出错误画面。

`Compile` 产出：

- `Levels`：拓扑分层，同层通道无依赖或只读共享，可并行执行；
- `FirstUse`/`LastUse`：每份资源的生命周期（首次/末次使用所在层）；
- `Allocation`：复用后的内存分配计划（资源 → 内存块）；
- `ExportPlan()`：把调度顺序与分配计划导出为 JSON，用于确定性比对。

执行：`compiled.Run(ctx, RunOptions{...})` 按层推进，层内并行，
`RunOptions.MaxConcurrency` 限制并发通道数；`ctx` 取消时及时退出，不会死锁。

## 资源生命周期语义

- 资源的**分配与释放由运行时自动决定**：通道首次写入前从 `Pool` 获取内存，
  生命周期（末次使用层）结束后自动归还；
- **生命周期不重叠**的资源复用同一块内存（编译期 `Allocation` 给出静态计划，
  运行时 `Pool` 对空闲块做动态复用）；
- `Pool.BudgetBytes` 设定内存预算，超限时 `Acquire` 返回可区分的
  `rendergraph.ErrResourceExhausted` 错误（向上传递，不崩溃）；
- 空闲块被池保留复用，长时间运行内存占用稳定不增长（有测试保证峰值恒定）；
- `Pipeline` 跨帧保留仍被引用的资源，使增量渲染（见下）可以跳过未受影响的通道。

## 延迟着色管线

`deferred.Pipeline` 在渲染图上搭建两个通道：

1. **geometry**：逐像素求交，把可见面的反照率 / 法线 / 深度 / 材质参数写入
   GBuffer 的多个目标；
2. **lighting**：读取 GBuffer，累加所有光源贡献（Blinn-Phong），写出最终颜色。

### GBuffer 布局可配置

`Config.Layout`（`GBufferLayout`）描述目标的数量、语义与格式，
格式支持 `RGBA8 / RGBA16F / RGBA32F / R32F`。布局变化会使几何与光照通道
的配置标识失效，触发对应资源重建与通道重执行。

### 分块光照

`Config.TileSize > 0` 时光照阶段按屏幕分块推进，每块只处理影响该块的光源
（光源影响球投影为屏幕圆做保守剔除，块内仍逐像素精确判断影响半径），
`Config.TileWorkers` 控制分块并行度。光源按 ID 固定顺序逐像素累加，
各块写不相交的像素区域，因此**分块结果与逐像素结果逐位一致**，
与分块大小、并行度、光源在场景中的给出顺序均无关。

### 管线变更

- `Pipeline.Reconfigure(cfg)`：修改配置后重新编译渲染图，按
  `ConfigKey` 与依赖传播计算脏通道集合——只有受影响的通道重执行，
  未受影响的通道直接复用保留的中间结果（如只改 `TileSize` 时仅重跑光照）；
- 也可以直接通过 `Pipeline.Graph()` 增删通道（`AddPass`/`RemovePass`），
  下一次 `Render` 会重新解析依赖；删除颜色产出者后渲染会报错而不是
  静默输出旧画面。

### 确定性与可复现

- 相同场景与配置下多次渲染输出逐位一致（浮点累加顺序固定、
  调度顺序由拓扑分层唯一确定）；
- `Pipeline.Compiled().ExportPlan()` 导出调度顺序与资源分配计划（JSON），
  可用于跨次运行比对。

## 配置项一览（`deferred.Config`）

| 字段 | 含义 | 默认/0 值语义 |
| --- | --- | --- |
| `Width, Height` | 渲染目标尺寸 | 必填 |
| `Layout` | GBuffer 布局（目标数量与格式） | `nil` 时用默认四目标布局 |
| `TileSize` | 分块光照块边长（像素） | `0` = 不分块，逐像素 |
| `TileWorkers` | 分块并行度 | `<=0` = 1 |
| `PassConcurrency` | 渲染图通道并发上限 | `<=0` = 不限制 |
| `BudgetBytes` | 中间结果内存预算 | `0` = 不限；超限时返回 `ErrResourceExhausted` |

资源紧张时的既定策略：**拒绝本次渲染**并返回可区分的
`rendergraph.ErrResourceExhausted` 错误，由调用方决定降级
（如降低分辨率/预算内重试），运行时不会死锁或长时间阻塞。

## 测试与运行

```sh
go test ./...   # 依赖排序、循环检测、分块一致性、资源复用、管线变更、确定性等
go run ./cmd/demo
```
