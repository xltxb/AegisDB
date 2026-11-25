package service

// 两个定时拨号者(连通性巡检、元数据同步)共用的并发骨架。
//
// 审查时它们是逐行相同的两份:信号量 + WaitGroup + Mutex + 三个只喂一行日志的计数器,
// 连同复制过来的怪癖 —— 主 goroutine 上给 skipped++ 加锁,以及 N 个 goroutine 一次性
// 起完再在信号量上排队(只限制了拨号数,没限制 goroutine 数,每个还各带一份含加密口令的
// Connection 拷贝)。
//
// 这里改成有界的工作池:concurrency 个 worker 从一个 channel 里取,goroutine 数是
// O(concurrency) 而不是 O(N);计数器是每个 worker 自己的局部变量,最后加总,不需要锁。
//
// 跳过规则也只在这一处过:skipAutoCheck 里那三条(维护态、没凭据、上次认证失败)对两个
// 拨号者同样成立 —— 防锁账号那条尤其如此,它漏在任何一个上都是同一起事故。

import (
	"sync"

	"aegisdb/internal/model"
)

// sweepTally 是一轮的账。
type sweepTally struct{ ok, bad, skipped int }

// forEachRealConn 对每台**该拨号的**实例并发做一件事。do 返回这台是否成功。
func (s *Services) forEachRealConn(concurrency int, do func(*model.Connection) bool) (sweepTally, error) {
	conns, err := s.Repo.ListConnections()
	if err != nil {
		return sweepTally{}, err
	}
	if concurrency < 1 {
		concurrency = 1
	}
	jobs := make(chan *model.Connection)
	local := make([]sweepTally, concurrency)
	var wg sync.WaitGroup
	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for c := range jobs {
				if do(c) {
					local[w].ok++
				} else {
					local[w].bad++
				}
			}
		}(w)
	}
	var total sweepTally
	for i := range conns {
		if skipAutoCheck(&conns[i]) != "" {
			total.skipped++
			continue
		}
		jobs <- &conns[i]
	}
	close(jobs)
	wg.Wait()
	for _, l := range local {
		total.ok += l.ok
		total.bad += l.bad
	}
	return total, nil
}
