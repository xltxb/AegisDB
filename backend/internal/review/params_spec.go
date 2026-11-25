package review

// 规则参数的说明书。
//
// 参数原先是一个 JSON 文本框:哪条规则认哪些键、留空时按什么跑,全写在各个检查器的字面量
// 里,控制台看不见。于是"表名命名规范"这类参数为空的规则,运维不知道它其实在按一条隐藏的
// 正则判,更不知道 Oracle 上换了一条;而本来就没有参数的规则也摆着同一个文本框,看着像是
// 漏配了。
//
// 这张表是参数的**唯一**来源:
//
//   - 控制台据它画表单(每个键一个输入框,占位符就是生效中的默认值);
//   - fire 在调检查器前把缺省的键按它补齐(withDefaults),所以"留空"= 这里写的默认值,
//     而不是某个检查器里谁也看不见的字面量;
//   - 保存时按它校验(ValidateParams),键名写错、正则编不过、数字填了字母都当场报出来。
//
// 默认值的取法:种子里写了的键取种子(Builtins.Params,那是规则出厂时的配置),种子没写的
// 键取这里的 Default。init 会把两者合起来,TestParamSpecsCoverSeeds 保证种子里的每个键都
// 在这里登记过。

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"velagateway/internal/apimsg"
)

// Param types the console knows how to render.
const (
	ParamInt   = "int"   // 正整数
	ParamText  = "text"  // 一段文字(前缀、引擎名)
	ParamRegex = "regex" // 正则表达式,保存时编译校验
	ParamList  = "list"  // 字符串列表,控制台一行一个
	ParamBool  = "bool"
	ParamEnum  = "enum" // 取 Options 之一
)

// ParamSpec describes one knob of one rule.
type ParamSpec struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Default any      `json:"default"`
	Options []string `json:"options,omitempty"`
	// DialectDefaults:某些方言上默认值不同(Oracle 标识符上限 30、表名允许大写)。
	DialectDefaults map[string]any `json:"dialectDefaults,omitempty"`
}

// RegexRuleParams is the pseudo-code under which custom (kind=regex) rules'
// knobs are published.
const RegexRuleParams = "_regex"

var paramSpecs = map[string][]ParamSpec{
	"ddl.forbid.drop": {
		{Key: "objects", Type: ParamList},
		{Key: "dropColumn", Type: ParamBool},
	},
	codeAlterMerge: {{Key: "max", Type: ParamInt}},
	"ddl.forbid.column.type": {{
		Key: "types", Type: ParamList,
		Default:         []string{"float", "double", "real", "blob", "longblob"},
		DialectDefaults: map[string]any{DialectOracle: []string{"float", "double", "real", "blob", "longblob", "long"}},
	}},
	"dws.forbid.column.type":  {{Key: "types", Type: ParamList}},
	"dws.avoid.column.type":   {{Key: "types", Type: ParamList}},
	"ddl.varchar.max.length":  {{Key: "max", Type: ParamInt}},
	"dws.require.precision":   {{Key: "types", Type: ParamList}},
	"dws.varchar.length":      {{Key: "max", Type: ParamInt}},
	"mysql.require.innodb":    {{Key: "engine", Type: ParamText}},
	"mysql.require.utf8mb4":   {{Key: "charset", Type: ParamText}},
	"dws.distribute.key.max":  {{Key: "max", Type: ParamInt}},
	"dws.distribute.key.type": {{Key: "types", Type: ParamList}},
	"dws.distribute.key.no.uuid.default": {{Key: "functions", Type: ParamList}},
	"dws.pk.max.columns":                 {{Key: "max", Type: ParamInt}},
	"dws.partition.max":                  {{Key: "max", Type: ParamInt}},
	"dws.view.max.nesting":               {{Key: "max", Type: ParamInt}},
	"naming.table.pattern": {{
		Key: "pattern", Type: ParamRegex,
		Default: `^[a-z][a-z0-9_]*$`,
		// Oracle 把不带引号的标识符折成大写,小写正则会把每张合规的表都报一遍。
		DialectDefaults: map[string]any{DialectOracle: `^[A-Za-z][A-Za-z0-9_$#]*$`},
	}},
	"tidb.table.prefix": {{Key: "pattern", Type: ParamRegex}},
	"naming.index.prefix": {
		{Key: "pkPrefix", Type: ParamText},
		{Key: "uniquePrefix", Type: ParamText},
		{Key: "indexPrefix", Type: ParamText},
	},
	"naming.index.max.length": {{Key: "max", Type: ParamInt}},
	"naming.identifier.length": {{
		Key: "max", Type: ParamInt, Default: 64,
		DialectDefaults: map[string]any{DialectOracle: 30}, // 12.2 以前;12.2+ 可改成 128
	}},
	"tidb.table.max.length": {{Key: "max", Type: ParamInt}},
	"naming.forbid.keyword": {{Key: "words", Type: ParamList, Default: defaultReserved}},
	"dws.object.name.charset":         {{Key: "pattern", Type: ParamRegex}},
	"dws.object.name.reserved.prefix": {{Key: "prefixes", Type: ParamList}},
	"dws.object.name.max.length":      {{Key: "max", Type: ParamInt}},
	"index.max.columns":               {{Key: "max", Type: ParamInt}},
	"index.max.per.table":             {{Key: "max", Type: ParamInt}},
	"dws.index.max.per.table":         {{Key: "max", Type: ParamInt}},
	"dws.index.btree.only":            {{Key: "methods", Type: ParamList}},
	"security.forbid.sensitive.column": {{Key: "patterns", Type: ParamList}},
	"dws.forbid.volatile.in.subquery":  {{Key: "functions", Type: ParamList}},
	"dws.max.join.tables":              {{Key: "max", Type: ParamInt}},

	RegexRuleParams: {
		{Key: "pattern", Type: ParamRegex, Default: ""},
		{Key: "mode", Type: ParamEnum, Default: "forbid", Options: []string{"forbid", "require"}},
		// scope=raw:对原文匹配(含字符串与注释);默认对去掉字面量后的语句匹配。
		{Key: "scope", Type: ParamEnum, Default: "sql", Options: []string{"sql", "raw"}},
	},
}

func init() {
	seeds := map[string]params{}
	for _, b := range Builtins {
		if b.Params != "" {
			var p params
			if err := json.Unmarshal([]byte(b.Params), &p); err != nil {
				panic(fmt.Sprintf("review: builtin %s has invalid params: %v", b.Code, err))
			}
			seeds[b.Code] = p
		}
	}
	for code, specs := range paramSpecs {
		for i := range specs {
			if v, ok := seeds[code][specs[i].Key]; ok {
				specs[i].Default = v
			}
		}
	}
}

// ParamSpecs returns the knob descriptions for every rule that has any. A code
// absent from the map has no knobs: its params are ignored.
func ParamSpecs() map[string][]ParamSpec { return paramSpecs }

// defaultFor is the value a knob runs with when the operator left it empty.
func (ps ParamSpec) defaultFor(dialect string) any {
	if v, ok := ps.DialectDefaults[dialect]; ok {
		return v
	}
	return ps.Default
}

// withDefaults fills every knob the operator left out with its documented
// default, so a checker's own literal is never what silently decides.
//
// "留空"判定与 params.str 一致:空字符串也算没填。显式写了的值(包括空列表)原样保留。
func withDefaults(code, dialect string, p params) params {
	specs, ok := paramSpecs[code]
	if !ok {
		return p
	}
	out := make(params, len(p)+len(specs))
	for k, v := range p {
		out[k] = v
	}
	for _, ps := range specs {
		v, has := out[ps.Key]
		if s, isStr := v.(string); has && !(isStr && strings.TrimSpace(s) == "") {
			continue
		}
		if d := ps.defaultFor(dialect); d != nil {
			out[ps.Key] = toParamValue(d)
		}
	}
	return out
}

// toParamValue normalises a Go-typed default into what json.Unmarshal would
// have produced, which is what params' accessors expect.
func toParamValue(v any) any {
	switch t := v.(type) {
	case []string:
		out := make([]any, len(t))
		for i, s := range t {
			out[i] = s
		}
		return out
	case int:
		return float64(t)
	}
	return v
}

// ValidateParams checks an operator's params against the rule's knobs. code is
// the rule code, or RegexRuleParams for a custom rule.
func ValidateParams(code, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var p map[string]any
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return apimsg.Errorf("规则参数不是合法 JSON 对象")
	}
	specs, ok := paramSpecs[code]
	if !ok {
		if len(p) > 0 {
			return apimsg.Errorf("此规则没有可调参数,参数请留空")
		}
		return nil
	}
	known := map[string]ParamSpec{}
	for _, ps := range specs {
		known[ps.Key] = ps
	}
	for k, v := range p {
		ps, ok := known[k]
		if !ok {
			return apimsg.Errorf("参数 %s 不是此规则认识的参数", k)
		}
		if err := checkParamValue(ps, v); err != nil {
			return err
		}
	}
	return nil
}

func checkParamValue(ps ParamSpec, v any) error {
	switch ps.Type {
	case ParamInt:
		n, ok := 0, false
		switch t := v.(type) {
		case float64:
			n, ok = int(t), t == float64(int(t))
		case string:
			i, err := strconv.Atoi(strings.TrimSpace(t))
			n, ok = i, err == nil
		}
		if !ok || n <= 0 {
			return apimsg.Errorf("参数 %s 须为正整数", ps.Key)
		}
	case ParamText, ParamRegex, ParamEnum:
		s, ok := v.(string)
		if !ok {
			return apimsg.Errorf("参数 %s 须为文本", ps.Key)
		}
		if ps.Type == ParamRegex && strings.TrimSpace(s) != "" {
			if _, err := regexp.Compile(s); err != nil {
				return apimsg.Errorf("参数 %s 的正则无效: %v", ps.Key, err)
			}
		}
		if ps.Type == ParamEnum && s != "" {
			for _, o := range ps.Options {
				if s == o {
					return nil
				}
			}
			return apimsg.Errorf("参数 %s 只能是 %s 之一", ps.Key, strings.Join(ps.Options, " / "))
		}
	case ParamList:
		arr, ok := v.([]any)
		if !ok {
			return apimsg.Errorf("参数 %s 须为文本列表", ps.Key)
		}
		for _, e := range arr {
			if _, ok := e.(string); !ok {
				return apimsg.Errorf("参数 %s 须为文本列表", ps.Key)
			}
		}
	case ParamBool:
		if _, ok := v.(bool); !ok {
			return apimsg.Errorf("参数 %s 须为 true 或 false", ps.Key)
		}
	}
	return nil
}
