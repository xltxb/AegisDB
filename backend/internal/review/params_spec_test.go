package review

import (
	"encoding/json"
	"strings"
	"testing"
)

// 参数说明书必须和种子、检查器对得上:种子里的每个键都登记过,登记过的规则都有实现,
// 每个键都有默认值(控制台要拿它当占位符),出厂参数本身也过得了保存校验。
func TestParamSpecsCoverSeeds(t *testing.T) {
	for _, b := range Builtins {
		if b.Params == "" {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal([]byte(b.Params), &p); err != nil {
			t.Fatalf("%s 的种子参数不是 JSON: %v", b.Code, err)
		}
		specs, ok := paramSpecs[b.Code]
		if !ok {
			t.Errorf("%s 有种子参数,但参数说明书里没登记 —— 控制台会把它当成无参数规则", b.Code)
			continue
		}
		for k := range p {
			found := false
			for _, ps := range specs {
				found = found || ps.Key == k
			}
			if !found {
				t.Errorf("%s 的种子参数 %s 没在说明书里登记", b.Code, k)
			}
		}
		if err := ValidateParams(b.Code, b.Params); err != nil {
			t.Errorf("%s 的出厂参数过不了保存校验: %v", b.Code, err)
		}
	}
	for code, specs := range paramSpecs {
		if code != RegexRuleParams {
			if _, ok := registry[code]; !ok {
				t.Errorf("说明书登记了 %s,但没有这条规则", code)
			}
		}
		for _, ps := range specs {
			if ps.Default == nil {
				t.Errorf("%s.%s 没有默认值,控制台无从告诉人留空时按什么跑", code, ps.Key)
			}
		}
	}
}

// 2026-11-14:"表名命名规范""标识符长度上限"等规则参数为空,运维不知道它实际按什么跑。
// 留空 = 说明书里的默认值,且按方言区分(Oracle 标识符 30)。
func TestEmptyParamsRunTheDocumentedDialectDefault(t *testing.T) {
	col31 := strings.Repeat("c", 31)
	hits(t, "naming.identifier.length", DialectOracle, "CREATE TABLE T1 ("+col31+" NUMBER)", col31)
	quiet(t, "naming.identifier.length", DialectMySQL, "CREATE TABLE t1 ("+col31+" int)")

	// Oracle 的表名允许大写,MySQL 默认只认小写。
	quiet(t, "naming.table.pattern", DialectOracle, "CREATE TABLE ORDERS (ID NUMBER)")
	hits(t, "naming.table.pattern", DialectMySQL, "CREATE TABLE Orders (id int)", "Orders")
}

// 合并默认值不能覆盖运维显式写的值。
func TestWithDefaultsKeepsWhatTheOperatorWrote(t *testing.T) {
	p := withDefaults("naming.identifier.length", DialectOracle, params{"max": float64(128)})
	if p.num("max", 0) != 128 {
		t.Fatalf("显式的 128 被默认值覆盖成了 %d", p.num("max", 0))
	}
	p = withDefaults("naming.identifier.length", DialectOracle, params{})
	if p.num("max", 0) != 30 {
		t.Fatalf("Oracle 留空应按 30 跑,实际 %d", p.num("max", 0))
	}
}

func TestValidateParams(t *testing.T) {
	cases := []struct {
		code, raw, wantErr string
	}{
		{"naming.identifier.length", `{"max":64}`, ""},
		{"naming.identifier.length", ``, ""},
		{"naming.identifier.length", `{"max":"abc"}`, "正整数"},
		{"naming.identifier.length", `{"max":0}`, "正整数"},
		{"naming.identifier.length", `{"maxx":64}`, "不是此规则认识的参数"},
		{"naming.table.pattern", `{"pattern":"^t_("}`, "正则无效"},
		{"naming.forbid.keyword", `{"words":"order"}`, "文本列表"},
		{"ddl.forbid.drop", `{"dropColumn":"yes"}`, "true 或 false"},
		{"dml.require.where", `{"max":1}`, "没有可调参数"},
		{"dml.require.where", `{}`, ""},
		{"dml.require.where", `[1]`, "JSON 对象"},
	}
	for _, c := range cases {
		err := ValidateParams(c.code, c.raw)
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s %s 应通过,却报 %v", c.code, c.raw, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s %s 应报含 %q 的错,实际 %v", c.code, c.raw, c.wantErr, err)
		}
	}
}

// naming.index.prefix 的 pkPrefix 原先填了也不生效。具名主键约束现在照它检查;
// 没起名字的主键不算违规。
func TestPrimaryKeyConstraintPrefix(t *testing.T) {
	hits(t, "naming.index.prefix", DialectTiDB,
		"CREATE TABLE t_a (id bigint, CONSTRAINT primary_a PRIMARY KEY (id))", "primary_a")
	hits(t, "naming.index.prefix", DialectDWS,
		"ALTER TABLE t_a ADD CONSTRAINT a_key PRIMARY KEY (id)", "a_key")
	quiet(t, "naming.index.prefix", DialectTiDB,
		"CREATE TABLE t_a (id bigint, CONSTRAINT pk_a PRIMARY KEY (id))")
	quiet(t, "naming.index.prefix", DialectMySQL,
		"CREATE TABLE t_a (id bigint, PRIMARY KEY (id))")
}

// TiDB 规范只限表名 32 字符;原先列名超 32 也被报成"表名不超过 32 个字符"。
func TestTiDBTableNameLengthOnlyMeasuresTheTable(t *testing.T) {
	long := strings.Repeat("c", 40)
	quiet(t, "tidb.table.max.length", DialectTiDB, "CREATE TABLE t_a ("+long+" int NOT NULL)")
	hits(t, "tidb.table.max.length", DialectTiDB, "CREATE TABLE t_"+long+" (id int NOT NULL)", "t_"+long)
}

// 有专属规则的库上,通用规则让位:同一件事不报两次,也不拿别的库的标准来判。
func TestGenericRulesYieldToDialectSpecificOnes(t *testing.T) {
	yield := map[string][]string{
		DialectDWS: {"ddl.varchar.max.length", "naming.table.pattern", "naming.identifier.length", "ddl.forbid.column.type"},
		DialectTiDB: {"naming.table.pattern"},
	}
	byCode := map[string]Builtin{}
	for _, b := range Builtins {
		byCode[b.Code] = b
	}
	for d, codes := range yield {
		for _, c := range codes {
			r := Rule{Code: c, Dialect: byCode[c].Dialect}
			if r.AppliesTo(d) {
				t.Errorf("%s 在 %s 上应让位给专属规则,但仍然适用", c, d)
			}
		}
	}

	// DWS:VARCHAR(5000) 在规范的 6000 以内,不该再被通用的 4000 报。
	res := Check(DialectDWS, "CREATE TABLE s.t_a (id int NOT NULL, note varchar(5000)) WITH (orientation=column)", allRules())
	if codes(res)["ddl.varchar.max.length"] {
		t.Error("DWS 上 VARCHAR(5000) 仍被通用的 4000 上限报了")
	}

	// DWS 规范列为高危/不支持的 JSON,由 DWS 专属规则以 error 报,且只报一次。
	res = Check(DialectDWS, "CREATE TABLE s.t_a (id int NOT NULL, doc json)", allRules())
	n := 0
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "JSON") {
			n++
			if f.Code != "dws.forbid.column.type" {
				t.Errorf("JSON 列应由 dws.forbid.column.type 报,却由 %s 报", f.Code)
			}
		}
	}
	if n != 1 {
		t.Errorf("JSON 列应恰好报一次,实际 %d 次", n)
	}

	// Oracle:VARCHAR 交给 oracle.prefer.varchar2,禁用类型那条不再重复报。
	quiet(t, "ddl.forbid.column.type", DialectOracle, "CREATE TABLE T1 (NAME VARCHAR(10))")
	hits(t, "oracle.prefer.varchar2", DialectOracle, "CREATE TABLE T1 (NAME VARCHAR(10))", "")
}
