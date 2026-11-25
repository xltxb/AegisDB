package model

// BindVar 是 SQL*Plus 的一个绑定变量(VARIABLE name type):名字(大写)、类型(如 NUMBER /
// VARCHAR2(30))、当前值。终端标签页记着它们,每次执行随请求带给网关,执行后带回新值
// (见 gateway/oracle_binds.go)。
type BindVar struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	Null  bool   `json:"null"`
}
