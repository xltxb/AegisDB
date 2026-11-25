package bootstrap

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	"gorm.io/gorm/schema"
)

// 布尔字段不许带非零的 GORM 默认值。
//
// GORM 建行时把零值字段换成 default 标签的值:带 default:true 的布尔列,"建一条 false 的"
// 存下来就是 true。2026-09 无 WHERE 开关、2026-11 元数据缓存的可空(每轮删插一半列)、
// 新建停用的发布流程 / 审查规则 / 班车窗口 / Webhook 都栽在这上面。库里的 DEFAULT
// 放在迁移里就够了,模型上不需要。
func TestModels_NoNonZeroBoolDefaults(t *testing.T) {
	for _, m := range allModels {
		s, err := schema.Parse(m, &sync.Map{}, schema.NamingStrategy{})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range s.Fields {
			if f.FieldType.Kind() != reflect.Bool && !(f.FieldType.Kind() == reflect.Ptr && f.FieldType.Elem().Kind() == reflect.Bool) {
				continue
			}
			if !f.HasDefaultValue {
				continue
			}
			if d := strings.ToLower(strings.Trim(f.DefaultValue, "'\" ")); d != "false" && d != "0" && d != "" {
				t.Errorf("%s.%s 带 default:%s —— 建行时 false 会被换成它,去掉这个标签", s.Name, f.Name, f.DefaultValue)
			}
		}
	}
}
