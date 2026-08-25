# @userinner/kern-sdk

Typed, dependency-free HTTP/SSE client for Kern Core.

```ts
import { KernClient } from '@userinner/kern-sdk'

const kern = new KernClient({
  baseURL: 'http://127.0.0.1:8787',
  token: process.env.KERN_TOKEN,
})

const task = await kern.createTask({ goal: 'Inspect this repository and run its checks.' })
for await (const event of kern.watchEvents(task.id)) {
  if (event.type === 'task.completed' || event.type === 'task.failed') break
}
```
