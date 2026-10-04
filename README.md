# deferred-render-graph-go

用 Go 实现的**延迟着色（Deferred Shading）软件渲染管线**。多个渲染通道
（Pass）及其对中间结果（G-Buffer、深度、最终颜色等）的读写依赖由
**渲染图（Render Graph）** 统一组织，运行时自动完成依赖解析、调度、
资源生命周期管理与复用，并保证在相同场景与配置下输出**确定且可复现**。

## 结构

```
graph/     渲染图核心：依赖解析、拓扑分层调度、内存池、增量执行
deferred/  延迟着色管线：几何通道（G-Buffer）、光照通道（逐像素/分块）
cmd/demo/  端到端示例：渲染一帧并导出 PPM 图像与调度轨迹
```

## 渲染图的描述方式

渲染图由**资源**与**通道**两类节点构成：

```go
g := graph.NewGraph()

// 1. 声明中间结果（名称 + 尺寸 + 格式）
g.DeclareResource("gbuffer0", graph.ResourceDesc{Width: 640, Height: 480, Format: graph.FormatRGB32F})
g.DeclareResource("finalColor", graph.ResourceDesc{Width: 640, Height: 480, Format: graph.FormatRGB32F})

// 2. 添加通道：声明它读什么、写什么、做什么
g.AddPass(&graph.Pass{
    Name:       "geometry",
    Writes:     []string{"gbuffer0"},
    ConfigHash: 1,                       // 配置指纹，用于管线变更后的增量执行
    Execute:    func(ctx *graph.PassContext) error { /* ... */ return nil },
})
g.AddPass(&graph.Pass{
    Name:    "lighting",
    Reads:   []string{"gbuffer0"},
    Writes:  []string{"finalColor"},
    Execute: func(ctx *graph.PassContext) error { /* ... */ return nil },
})

// 3. 编译：依赖解析 + 拓扑分层 + 生命周期分析
compiled, err := g.Compile() // 循环依赖会返回带通道名路径的错误
```

依赖规则（对每个资源按通道声明顺序分析）：

| 依赖 | 规则 |
|------|------|
| RAW  | 读者排在其消费的写者之后 |
| WAW  | 同一资源的多个写者按声明顺序排列 |
| WAR  | 写者排在消费上一写者结果的读者之后 |

编译结果 `Compiled` 把通道分成若干**层（Level）**：同层通道互不依赖
（无依赖或只读共享），可并行推进；层与层之间顺序执行。出现循环依赖时，
`Compile` 返回形如 `graph: cyclic dependency detected: A -> B -> C -> A`
的可定位错误，不会静默产出错误画面。

## 执行与调度

```go
sched := graph.NewScheduler(graph.SchedulerOptions{
    MaxParallelPasses: 4,          // 通道级并发上限
    MemoryBudgetBytes: 1 << 30,    // 中间结果内存预算，<=0 不限
})
fingerprints, trace, buffers, err := sched.Run(compiled, nil) // 首帧 prev 传 nil
```

- 同层通道以信号量限制并发度并行执行，层间以屏障同步，**不会死锁**。
- `trace` 记录本次渲染的调度顺序（`exec`/`skip`）与每次资源分配
  （来源 `new`/`pool`/`retained`、字节数），可导出用于比对（见 demo）。
- 内存不足时返回可用 `errors.Is(err, graph.ErrOutOfMemory)` 判断的
  **可区分失败**，错误信息包含资源名与所需字节数，不会崩溃。

## 资源生命周期语义

- 编译期为每个资源计算生命周期区间 `[First, Last]`（按调度层计）。
- 资源在其 `First` 层开始前分配，在 `Last` 层结束后归还内存池；
  **生命周期不重叠的资源复用同一块内存**。
- 运行结束时，内容仍然完好的缓冲区被**跨帧保留**（retained）；
  下次运行时干净通道的输出可直接沿用。
- 若保留内容因内存复用而失效，调度器会自动把写出它的通道标脏重跑，
  保证结果与全量执行一致。
- 内存池只持有有限的空闲块与保留块（以图中资源数为界），
  **长时间运行内存占用不增长**。

## 延迟着色管线

`deferred.NewPipeline(scene, layout, opts)` 组装两个通道：

1. **geometry**：把可见面的位置、法线、反照率（可选：材质参数）写入
   G-Buffer 多目标中间结果，同时写深度。
2. **lighting**：基于 G-Buffer 完成 Blinn-Phong 着色，写出最终颜色。

### G-Buffer 布局可配置

```go
layout := deferred.DefaultLayout(w, h) // 位置 RGB32F + 法线 RGB32F + 反照率 RGBA8
layout =  deferred.FullLayout(w, h)    // 追加材质参数 R32F
// 或完全自定义目标数量与格式：
layout = deferred.GBufferLayout{Width: w, Height: h, Targets: []graph.Format{...}}
```

### 分块光照

```go
opts := deferred.LightingOptions{
    TileSize:    16, // 屏幕分块边长；<=1 表示逐像素处理
    TileWorkers: 4,  // 分块并行度；<=1 表示串行
    Ambient:     0.06,
}
```

分块模式下，每块先根据块内深度范围反投影出世界空间包围盒，
**保守剔除**不可能影响该块的光源（被剔除光源的贡献恰为 0），
再逐像素着色。每个像素的光源按 `Light.ID` 升序累加，因此：

- 分块拼接结果与逐像素处理**逐位一致**（有测试保证）；
- 结果与分块大小、并行度、光源在场景中的排列顺序无关。

## 管线变更

运行中可以增删通道或修改配置：

```go
g.AddPass(newPass)        // 同名通道被替换（修改配置）
g.RemovePass("invert")    // 删除通道
compiled2, _ := g.Compile()                       // 重新解析依赖
fp2, trace2, _, _ := sched.Run(compiled2, fp1)    // 传入上次指纹
```

增量执行规则：配置指纹（`ConfigHash`）变化、依赖被级联影响、或输出
内容已失效的通道会重新执行；**未受影响的通道跳过**（trace 中记为
`skip`），其输出来自跨帧保留的内容，画面与全量重跑一致。

## 确定性与可复现

- 调度顺序确定：层序 + 层内按通道名排序。
- 分配顺序确定：资源按名称排序分配。
- 光照累加顺序确定：光源按 ID 排序，与场景中的存储顺序无关。
- 几何光栅化在通道内串行，z-buffer 无竞争。
- 相同场景与配置下多次渲染同一帧，输出**逐位一致**；
  `Trace` 可导出用于跨次运行比对（见 `cmd/demo` 的 `-trace`）。

## 性能与资源约束

| 配置 | 位置 | 语义 |
|------|------|------|
| `SchedulerOptions.MaxParallelPasses` | 调度器 | 并发执行的通道数上限 |
| `SchedulerOptions.MemoryBudgetBytes` | 调度器 | 中间结果内存预算，超限返回 `ErrOutOfMemory` |
| `LightingOptions.TileSize` | 光照通道 | 分块大小 |
| `LightingOptions.TileWorkers` | 光照通道 | 分块并行度 |

资源紧张时按既定策略**拒绝**（返回 `ErrOutOfMemory`），
已分配资源会被回收，不会死锁或长时间阻塞。

## 运行测试与示例

```sh
go test ./...        # 全部测试（含 -race 可用 go test -race ./...）
go run ./cmd/demo -w 160 -h 120 -tile 16 -workers 4 -o out.ppm -trace trace.txt
```

测试覆盖：依赖排序、并行分层、循环依赖检测、未声明资源、
分块与逐像素一致性、跨次运行确定性、光源顺序无关性、
G-Buffer 布局可配置、资源复用、内存预算失败、内存不增长、
并发上限、管线变更（修改/新增/删除通道）与轨迹导出比对。
