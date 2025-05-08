
1. 淘汰策略支持  （1）LRU - 最近最少使用淘汰策略（2） LFU - 最不常用淘汰策略 （3）FIFO - 先进先出淘汰策略（4）Random - 随机淘汰策略（5）TTL - 仅基于过期时间淘汰
2. 支持设置淘汰时间，超时自动淘汰。
3. 支持在N秒内，访问M次，重置超时时间。
4. 支持设置内存上限
5. 支持设置数据项数量。
6. 支持淘汰数据通知。 需要知道淘汰原因	(1) EvictionReasonExpired - 项目因TTL过期而被淘汰 (2) EvictionReasonCapacity - 项目因容量限制而被淘汰 (3)EvictionReasonMemory - 项目因内存限制而被淘汰 (4)EvictionReasonManual - 项目被手动移除
7. 支持分布式同步
   8.内存中储存的map 使用分片进行保存，减少锁的粒度，同时锁也是分段锁
9. key 和value支持范型
   10.支持批量获取
   11.支持多集缓存
   12.支持fallback，如果cache中不存在则请求fallback函数
13. fallback 支持多集缓存，如果local cache 不存在，先fallback到远程cache，如果远程cache也不存在，则返回fallback函数
14. 支持多点fallback，功能同12，和13
15. 远程同步和分布式同步不是同一个功能，远程同步类似于一级缓存，比如redis。分布式同步是多个实例节点需要数据一致。 如果local cache 不存在我使用获取一级缓存比如redis，如果redis中也不存在则使用fallback获取后，同步到redis 和 localcache 并且同步到所有的节点
16. 实现更多的淘汰策略
17. 添加监控和指标收集
    18.本地缓存优先，减少远程调用
19. 支持批量操作减少网络开销