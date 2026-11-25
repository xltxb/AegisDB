package model

import "gorm.io/gorm"

// 存库提示文本的 ref(迁移 0060)读出时补齐。
//
// 带参数的句子写入时连同 ref 存进 *_ref 列;固定句不存,读出来按原句查表 —— 老数据里的固定句
// 因此也能翻译。查表在 apimsg 包(它依赖 model,model 不能反过来依赖它),由 apimsg 启动时注入。
// 没注入(单测只用 model 时)就不补,界面回落到中文原串。

// StoredTextRef(text, refJSON) 返回该写进 JSON 的 ref:refJSON 非空原样返回,否则按原句查表。
var StoredTextRef = func(text, refJSON string) string { return refJSON }

func (n *Notification) AfterFind(*gorm.DB) error {
	n.TitleRef = StoredTextRef(n.Title, "")
	n.BodyRef = StoredTextRef(n.Body, n.BodyRef)
	return nil
}

func (j *ExportJob) AfterFind(*gorm.DB) error {
	if j.Error != "" {
		j.ErrorRef = StoredTextRef(j.Error, j.ErrorRef)
	}
	return nil
}

func (j *AsyncJob) AfterFind(*gorm.DB) error {
	if j.Error != "" {
		j.ErrorRef = StoredTextRef(j.Error, j.ErrorRef)
	}
	return nil
}

func (j *OSCJob) AfterFind(*gorm.DB) error {
	if j.Error != "" {
		j.ErrorRef = StoredTextRef(j.Error, j.ErrorRef)
	}
	if j.ThrottleReason != "" {
		j.ThrottleReasonRef = StoredTextRef(j.ThrottleReason, j.ThrottleReasonRef)
	}
	return nil
}

func (a *Approval) AfterFind(*gorm.DB) error {
	if a.Result != "" {
		a.ResultRef = StoredTextRef(a.Result, a.ResultRef)
	}
	return nil
}
