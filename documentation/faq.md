### What if my sectors get stuck at SubmitPreCommitBatch state?

```shell
./lotus-miner sectors batching precommit --publish-now
```

### What if my sectors get stuck at SubmitCommitAggregate state?

Turn off aggregate, or
```shell
./lotus-miner sectors batching commit --publish-now
```

### What if my deal get stuck at StorageDealPublish state?

```shell
./lotus-miner storage-deals pending-publish --publish-now
```

### What if my deal get stuck at StorageDealAwaitingPreCommit state?

```shell
./lotus-miner sectors batching precommit --publish-now
```
