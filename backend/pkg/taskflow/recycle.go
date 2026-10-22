package taskflow

import "errors"

// A protected runtime has not confirmed deletion. Never mark it recycled merely
// because the policy queue exhausted its normal remote-delete retries.
var ErrRecycleDeferred = errors.New("工作空间尚不能安全回收")

// Waiting for a healthy in-flight save is distinct from a failed save/delete.
var ErrRecyclePending = errors.New("正在等待工作空间保存或当前任务结束")

var ErrCheckpointBusy = errors.New("工作空间正在执行或保存，稍后再保存")
