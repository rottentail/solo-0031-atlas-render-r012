# HTTP 协议

所有业务路径以 `/v1` 开头。请求体使用 JSON，素材上传直接使用 PNG 字节。

| 方法和路径 | 请求 | 结果 |
|---|---|---|
| PUT /scenes/{id} | name, expectedRevision, document | 完整场景，200 |
| GET /scenes/{id} | 无 | 完整场景，200 |
| POST /scenes/{id}/state | expectedRevision, state | 完整场景，200 |
| GET /scenes/{id}/components | 无 | 组件采用状态，200 |
| POST /scenes/{id}/components/{component}/adopt | expectedRevision, revision, expectedComponentRevision | 完整场景，200 |
| PUT /components/{id} | name, expectedRevision, document | 组件及全部修订，200 |
| GET /components/{id} | 无 | 组件，200 |
| POST /components/{id}/state | expectedRevision, state | 组件，200 |
| POST /scenes/{id}/snapshots | expectedRevision | 快照和素材闭包，201 |
| PUT /assets/{id} | PNG字节 | 素材及base64字节，201 |
| POST /assets/{id}/retire | 无 | state，200 |
| GET /snapshots/{id} | 无 | 快照，200 |
| POST /snapshots/{id}/revoke | 无 | state，200 |
| GET /snapshots/{id}/package | 无 | atlas-scene交换包，200 |
| POST /imports/{id} | name, package | 新场景，201 |
| POST /jobs | snapshotId, scale；请求头Idempotency-Key | 任务，202 |
| GET /jobs/{id} | 无 | 任务，200 |
| POST /jobs/{id}/cancel | 无 | 任务，200 |
| POST /jobs/{id}/retry | 无 | 任务，202 |
| GET /artifacts/{id} | 可选If-None-Match | PNG，200或304 |
| DELETE /artifacts/{id} | 无 | state，200 |
| POST /compare | left, right（结果ID） | 像素比较，200 |
| GET /status | 无 | 场景/素材/组件/快照/任务计数和结果字节数 |
| GET /events | 无 | 最多500个有序领域事件 |

`GET /healthz` 不带 `/v1`，返回ready。它表明进程已打开并恢复状态，不包含单独的磁盘可写探测。任务响应省略固定输入的有效载荷，输入细节通过快照接口读取。

文档字段：width、height、background、layers。每个图层有id、kind、width、height、transform、opacity、fill；transform包括x、y、scaleX、scaleY、degrees。图层可以有clip（x/y/width/height），group使用children，polygon使用points，image使用assetId。solid颜料有color；linear还包括endColor、from、to。示例是可直接提交的完整请求。

## 可复用组件

组件是跨场景复用的图层片段。`PUT /v1/components/{id}` 的body为`name`、`expectedRevision`、`document`，其中document只有`layers`。首次提交`expectedRevision: 0`创建修订1；之后必须提交当前修订号，成功则追加一个不可变的新修订。较早修订永久保留，供既有快照重放。

图层`kind: "component"`表示实例，字段为`componentId`、`componentRevision`以及该实例自己的transform、opacity和clip。实例可出现在组内，也可出现在组件内部形成嵌套。实例自身不渲染图形，而是把组件修订的图层按实例的变换和局部裁剪展开，因此同一组件可在不同位置、角度和缩放下出现多次。

```json
{"id": "badge-one", "kind": "component", "componentId": "badge", "componentRevision": 1,
 "transform": {"x": 20, "y": 20, "scaleX": 1, "scaleY": 1, "degrees": 0}, "opacity": 1}
```

场景只记录固定修订（pin），不会自动跟随组件最新版本。`GET /v1/scenes/{id}/components` 返回使用的组件、当前pin、实例数量、是否有可用更新，以及来自嵌套组件的indirect出现。`POST /v1/scenes/{id}/components/{component}/adopt` 的body为`expectedRevision`（场景当前修订）、`revision`（目标组件修订）、可选`expectedComponentRevision`；它在一个事务内核对场景修订并把该组件的所有直接pin改到目标修订，递增场景修订并记录`componentAdoptions`。并发编辑中只有一方成功，另一方得到409，不会发布混用修订的文档。已位于目标修订时重放不新增修订。采用只向前，不能把活动场景回退到旧修订。

普通`PUT /scenes/{id}`不能为已存在的实例改换`componentId`或`componentRevision`，也不能新引入已停用组件；更新必须通过adopt。按未使用图层ID新增实例属于普通创作，可选择任意已发布修订。尝试改写现有pin返回409。

组件`active ↔ disabled`。停用后拒绝新修订、采用、包含它的新快照和新场景引用，但已有快照照常渲染其固定闭包，与素材退役语义一致。嵌套闭包中的停用同样阻止新引用。循环引用（含自引用和相互引用）返回400并给出链路；展开后的层数与嵌套深度仍受200层、8层限制。

冻结快照会捕获可达的全部组件修订（`snapshot.components`，键为`id@revision`）以及所有组件引用的素材字节。快照摘要覆盖文档、素材闭包和组件闭包；之后的组件修订、停用或素材退役都不改变它。

## 交换与导入

交换包的`snapshot.components`携带完整组件闭包，导入时原子写入场景、素材和所需组件修订。目标已存在同ID组件时，必须已有相同修订且内容一致，且组件可用，否则整体409。篡改组件内容或pin会因快照摘要不匹配而返回400。

幂等键长度1–80，使用英文字母、数字、下划线和连字符，首位不能是后两者。它在服务实例持久状态中全局生效，不随任务完成、取消、结果清理或进程重启失效。改换请求语义需使用新键。

服务接受相同内容的素材重复上传，退役素材不接受重新上传。交换包导出保留快照状态；导入会创建全新的active场景，原快照不作为目标服务中的快照记录落地。导入目标ID必须未使用；共享素材必须内容相同且仍可用。
