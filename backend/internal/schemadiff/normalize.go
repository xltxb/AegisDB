package schemadiff

import (
	"regexp"
	"strings"
	"unicode"
)

// 归一化:写法不同、意思相同的,比较前先化成同一个样子(PRD 3.4)。报告里显示的仍是原文。
//
// 每条规则都有"应视为相同"与"应视为不同"两个用例(diff_test.go 的 TestDiff_Normalization)——
// 归一化过头,真差异就被抹掉了,那比报出假差异更糟。

type family int

const (
	famOther family = iota
	famMySQL
	famOracle
	famPostgres
	famSQLite // 开发 / 测试环境用
)

func familyOf(engine string) family {
	e := strings.ToLower(engine)
	switch {
	case strings.Contains(e, "mysql"), strings.Contains(e, "mariadb"), strings.Contains(e, "tidb"), strings.Contains(e, "polardb"):
		return famMySQL
	case strings.Contains(e, "oracle"):
		return famOracle
	case strings.Contains(e, "postgre"), strings.Contains(e, "dws"), strings.Contains(e, "gauss"):
		return famPostgres
	case strings.Contains(e, "sqlite"):
		return famSQLite
	}
	return famOther
}

// FamilyName 是引擎族的名字(mysql / oracle / postgres / sqlite);不支持对比的引擎为空串。
// 只有同一族的两个库能互比。
func FamilyName(engine string) string {
	return [...]string{famOther: "", famMySQL: "mysql", famOracle: "oracle", famPostgres: "postgres", famSQLite: "sqlite"}[familyOf(engine)]
}

// SameFamily 报告两个引擎能不能互相比较(MySQL 系之间可以,MySQL 与 Oracle 不行)。
func SameFamily(a, b string) bool {
	fa, fb := familyOf(a), familyOf(b)
	return fa == fb && fa != famOther
}

// nameKey 是对象名的比较键。MySQL 的表名 / 列名 / 索引名在常见部署里不区分大小写
// (lower_case_table_names 不同的两台实例,目录里的大小写可能不一样);Oracle / PG 的目录里
// 已经是真名,大小写不同就是两个对象。
func nameKey(f family, name string) string {
	if f == famMySQL {
		return strings.ToLower(name)
	}
	return name
}

var (
	spaceRe = regexp.MustCompile(`\s+`)
	// MySQL 整数的显示宽度:int(11) 与 int 是同一个类型(8.0.17 起目录里干脆不写)。
	// tinyint(1) 例外 —— 它是布尔的约定写法,改动是有意义的。
	intWidthRe = regexp.MustCompile(`^(tinyint|smallint|mediumint|int|integer|bigint)\(\d+\)`)
	// PG 默认值上的类型转换:'new'::character varying、0::numeric。
	pgCastRe = regexp.MustCompile(`(::[a-z_ ]+(\[\])?(\(\d+(,\d+)?\))?)+$`)
)

func normType(f family, t string) string {
	// 只把引号外的部分转小写:类型关键字的大小写无所谓,enum('A','B') 里的取值大小写有所谓。
	s := lowerOutsideQuotes(strings.TrimSpace(t))
	s = spaceRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "( ", "(")
	s = strings.ReplaceAll(s, " )", ")")
	s = strings.ReplaceAll(s, ", ", ",")
	switch f {
	case famMySQL:
		// zerofill 时显示宽度决定补几个 0,是真差异,不去掉。
		if !strings.HasPrefix(s, "tinyint(1)") && !strings.Contains(s, "zerofill") {
			s = intWidthRe.ReplaceAllString(s, "$1")
		}
		// on update 并进了类型比较,里面的时间函数写法也要归一:MySQL 给 CURRENT_TIMESTAMP,
		// MariaDB 给 current_timestamp()。带精度的保留精度(3 ≠ 不带)。
		s = onUpdateFspRe.ReplaceAllString(s, "on update current_timestamp($2)")
		s = onUpdateRe.ReplaceAllString(s, "on update current_timestamp")
	case famOracle:
		s = strings.ReplaceAll(s, "number(*)", "number")
	}
	return s
}

func lowerOutsideQuotes(s string) string {
	var b strings.Builder
	quoted := false
	for _, r := range s {
		if r == '\'' {
			quoted = !quoted
		}
		if !quoted {
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}

// 各引擎默认值里的时间函数:键是小写写法,值是归一之后的比较值。只合并**真正等价**的写法 ——
// Oracle 的 SYSDATE 与 SYSTIMESTAMP、PG 的 LOCALTIMESTAMP 与 now() 是不同的东西,不能合并。
// 不在表里的默认值原样比较(区分大小写:MySQL 8 的字符串默认值在目录里不带引号,'New' 与
// 'new' 是真差异)。
var timeDefaults = map[family]map[string]string{
	famMySQL: {
		"current_timestamp": "current_timestamp", "current_timestamp()": "current_timestamp", "now()": "current_timestamp",
		"localtime": "current_timestamp", "localtime()": "current_timestamp",
		"localtimestamp": "current_timestamp", "localtimestamp()": "current_timestamp",
		"current_date": "current_date", "curdate()": "current_date",
	},
	famPostgres: {
		"now()": "current_timestamp", "current_timestamp": "current_timestamp", "transaction_timestamp()": "current_timestamp",
		"localtimestamp": "localtimestamp", "current_date": "current_date", "clock_timestamp()": "clock_timestamp()",
	},
	famOracle: {
		"sysdate": "sysdate", "systimestamp": "systimestamp", "current_timestamp": "current_timestamp",
		"current_date": "current_date", "localtimestamp": "localtimestamp", "sys_guid()": "sys_guid()", "user": "user",
	},
	famSQLite: {"current_timestamp": "current_timestamp", "current_date": "current_date", "current_time": "current_time"},
}

// 类型里并进来的 on update(见 gateway.mysqlExtra),类型已经转过小写。
var (
	onUpdateFspRe = regexp.MustCompile(`on update (current_timestamp|now|localtime|localtimestamp)\((\d+)\)`)
	onUpdateRe    = regexp.MustCompile(`on update (current_timestamp|now|localtime|localtimestamp)(\(\))?`)
)

// MySQL 带精度的时间函数:CURRENT_TIMESTAMP(3) = NOW(3),但 ≠ CURRENT_TIMESTAMP。
var mysqlTimeFspRe = regexp.MustCompile(`^(current_timestamp|now|localtime|localtimestamp)\((\d+)\)$`)

// normDefault 返回默认值的比较值;第二个返回值表示"有没有默认值"。
func normDefault(f family, c Column) (string, bool) {
	if !c.HasDefault {
		return "", false
	}
	d := strings.TrimSpace(c.Default)
	if f == famPostgres {
		d = pgCastRe.ReplaceAllString(d, "") // 先去类型转换:NULL::character varying 也是 NULL
	}
	if strings.EqualFold(d, "null") {
		if c.Nullable {
			return "", false // 可空列上"默认 NULL"就是没有默认值
		}
		return "null", true
	}
	l := strings.ToLower(d)
	if v, ok := timeDefaults[f][l]; ok {
		return v, true
	}
	if f == famMySQL {
		if m := mysqlTimeFspRe.FindStringSubmatch(l); m != nil {
			return "current_timestamp(" + m[2] + ")", true
		}
	}
	// 去掉一层引号:'0' 与 0 在数值列上是同一个默认值;字符串列上目录本来就带不带引号因引擎而异。
	if len(d) >= 2 && d[0] == '\'' && d[len(d)-1] == '\'' {
		return d[1 : len(d)-1], true // 整个是字符串字面量:区分大小写
	}
	// 表达式 / 关键字:Oracle、PG、SQLite 的目录里引号外的大小写只是写法(Oracle 存的是建表时的原文,
	// SYSTIMESTAMP(6) 与 systimestamp(6) 是同一个),引号里的仍区分。MySQL 例外 —— 它的字符串
	// 默认值在目录里不带引号,转小写会抹掉 'New' 与 'new' 的差异。
	if f != famMySQL {
		return lowerOutsideQuotes(d), true
	}
	return d, true
}

func normComment(s string) string { return strings.TrimSpace(s) }

func showNullable(n bool) string {
	if n {
		return "NULL"
	}
	return "NOT NULL"
}

// showDefault 是默认值的原文;没有默认值时为空串(不写死"(无)":英文界面会露出中文,由前端按语言显示)。
// 默认值本身是空串时写成两个单引号,与"没有默认值"区分开。
func showDefault(c Column) string {
	if !c.HasDefault {
		return ""
	}
	if c.Default == "" {
		return "''"
	}
	return c.Default
}
