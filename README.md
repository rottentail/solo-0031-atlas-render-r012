# Atlas Render

Go 静态图形合成服务。使用有序场景图描述矩形、椭圆、多边形和位图，在服务端输出 PNG。支持局部变换、嵌套裁剪、线性渐变、透明叠加、跨场景可复用组件、冻结快照、异步执行和快照包交换。仅使用 Go 标准库。

## 启动

需要 Go 1.22 或以上；持久存储的进程独占锁适用于 macOS 和 Linux。

```sh
go build ./...
go run ./cmd/atlas
```

默认监听 `127.0.0.1:8763`，状态写入 `var/atlas-state.json`。可用 `ATLAS_ADDR`、`ATLAS_STATE` 改变监听地址和状态路径。同一个状态路径只能由一个进程打开。服务启动会验证持久数据并恢复中断的执行任务。默认部署面向受信网络中的单实例生产流程。

## 运行真实工作流

```sh
python3 scripts/walkthrough.py --workflow submit-render --out var/demo
python3 scripts/walkthrough.py --workflow retire-asset
python3 scripts/walkthrough.py --workflow cancel-render
python3 scripts/walkthrough.py --workflow retry-render
python3 scripts/walkthrough.py --workflow recover-render
python3 scripts/walkthrough.py --workflow import-package
python3 scripts/walkthrough.py --workflow compare-renders
python3 scripts/walkthrough.py --workflow components-adopt
python3 scripts/walkthrough.py --workflow components-freeze-exchange
```

脚本在临时状态目录启动实际服务，通过 HTTP 完成操作后停止服务，输出任务状态和 PNG 摘要；`--out` 保存可查看的合成示例。恢复流程会强制结束脚本自己启动的服务进程，再验证恢复结果。所有工作流编号见 `.project-manifest.json`。

## 场景到结果

1. `PUT /v1/scenes/emblem`，提交名称、`expectedRevision: 0` 和 `document`。示例见 `examples/emblem.json`。
2. `POST /v1/scenes/emblem/snapshots`，提交 `expectedRevision`，取得快照 ID。
3. `POST /v1/jobs`，提交 `snapshotId`、`scale: 1`，并带唯一的 `Idempotency-Key` 请求头。
4. `GET /v1/jobs/{id}` 等待 `succeeded`，再读取 `GET /v1/artifacts/{artifactId}`。

同一幂等键和同一请求返回原任务；同键不同请求返回 409。快照包含引用素材的字节，后续场景编辑或素材退役不改变它。撤回快照会阻止新工作，已接受的任务仍使用固定输入。任务取消和结果提交竞争同一个持久状态；终态完成后不可取消。

## 可复用组件

1. `PUT /v1/components/badge`，提交 `name`、`expectedRevision: 0` 和只含 `layers` 的 `document`，得到修订 1。
2. 场景图层写 `{"kind": "component", "componentId": "badge", "componentRevision": 1, ...}`，实例的 transform、opacity 和 clip 作用在组件内容外侧。
3. 更新组件时提交 `expectedRevision: 1`，得到不可变的修订 2。既有场景和快照仍固定修订 1。
4. `GET /v1/scenes/emblem/components` 查看是否有可用更新；`POST /v1/scenes/emblem/components/badge/adopt` 提交场景当前修订号和目标修订，在一个事务内把该组件的所有直接 pin 前移。
5. 冻结快照捕获完整的组件闭包和素材闭包，因此导出包在别处也能重现相同像素。

采用是显式动作：组件发布新修订不会悄悄改变任何场景或快照。普通场景编辑不能改 pin，更新只能通过 adopt；同一场景的 adopt 与编辑并发时，只有一方成功，另一方得到 409，不会出现半新半旧的文档。停用组件只阻止前向生产（新修订、采用、新引用、新快照），已冻结内容仍照常渲染。循环引用返回 400 并指出链路。嵌套组件在冻结时一并捕获，但内层 pin 属于外层组件修订，场景无法单独 adopt 它；这样的请求返回 409，而不是产生混用版本。

## 范围与限制

画布每边最多 1536 像素，输出缩放为 1 或 2；场景不超过 200 图层、8 层嵌套。组件展开后同样受这两个上限约束，展开后的资产与组件闭包一并计入快照。PNG 素材每边最多 1024 像素、输入最多 2 MiB。编译后的工作量不得超过四千万个图层像素，队列最多接受 64 个未结束任务。

透明度为 0–1，变换中的 `scaleX`、`scaleY` 必须显式设置；省略会成为零而被拒绝。组的透明度乘到每个后代，属于分布式透明语义；当前没有离屏隔离组。缩放 2 表示两倍尺寸渲染，不表示输出降采样。位图按最近邻采样，颜色直接在编码通道上混合。

持久化使用完整 JSON 状态的原子替换，保留全部任务、快照与组件修订，事件只保留最近 500 条。组件修订只增不改，长期积累会增加状态体积；部署应按复用数量规划。适合中小规模、单写实例工作负载。清理结果只释放 PNG 字节，仍保留任务事实和摘要。进程重启可恢复任务；突然断电的文件系统耐久性不作额外保证。

测试处于 deferred 阶段，基线未包含自动化测试套件或 `test_command`。生产工作流检查、构建与静态检查已单独提供。接口和持久状态的规则详见 `PROJECT_SPEC.md`、`docs/api.md`、`docs/operations.md`。
