package gateway

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// 线上真实的一张宽表的选择列(175 列,含两个 AS 别名)
const realCols = "id,gbraid,market_referrer_gclid,wbraid,gclid,search_term,adid,android_id,android_id_md5,att_status,revenue_usd,revenue_usd_cents,city,revenue_cny,revenue_cny_cents,country,country_subdivision,deeplink,device_model,device_name,device_type,environment,epic_id,event_cost_id,event_name,event_token AS event,external_device_id_md5,fire_adid,first_install_time,lifetime_session_count,gps_adid,gps_adid_md5,idfa,idfa_md5,idfa_md5_hex,idfa_or_android_id,idfa_or_gps_adid,idfa_or_gps_adid_or_fire_adid,idfa_upper,idfv,idfv_or_google_app_set_id,ip_address,is_cross_device,is_default_tracker,is_imported,is_probabilistic,is_reattributed,isp,lang AS language,last_install_time,mac_md5,mac_sha1,match_type,mcc,mnc,switch_id,nonce,oaid,oaid_md5,currency,revenue_float,revenue,os_name,os_version,partner_parameters,publisher_parameters,customer_id,postal_code,preinstall_method,psn_uid,push_token,pv_passed,pv_status,random_user_id,referrer,reftag,src_reftag,region,reporting_currency,reporting_revenue,rida,sdk_version,session_count,sim_slot_ids,steam_id,steam_uuid,system_boot_time,third_party_sharing_disabled,tifa,time_spent,timezone,tracking_enabled,tracking_limited,user_agent,vida,web_uuid,win_adid,win_hwid,win_naid,win_udid,xuid,attribution_expires_at,callback_ttl,last_fallback_time,last_fallback_time_hour,platform,within_callback_ttl,activity_kind,adgroup_name,adgroup_tracker,app_name,app_name_dashboard,app_version,app_version_raw,app_version_short,campaign_name,campaign_tracker,click_time,click_time_hour,connection_type,conversion_duration,cpu_type,created_at,created_at_hour,created_at_milli,creative_name,creative_tracker,device_atlas_id,device_manufacturer,engaged_ad_based,engagement_time,engagement_time_hour,hardware_name,impression_based,impression_time,impression_time_hour,install_finish_time,first_tracker,first_tracker_name,installed_at,installed_at_hour,is_organic,is_s2s,last_session_time,last_tracker,last_tracker_name,network_name,network_tracker,proxy_ip_address,random,reattributed_at,reattributed_at_hour,received_at,reinstalled_at,rejection_reason,san_engagement_times,app_id,store,tracker_name,tracker,uninstalled_at,unverified_reason,dcp_ttclid,dcp_gclid,dcp_fbclid,iad_ad_id,iad_conversion_type,iad_country_or_region,iad_keyword,iad_keyword_id,iad_org_id,iad_supply_placement,click_id,src_file,gmt_create"

// 宽表(175 列)× 大量脱敏规则:打码目标的计算须在毫秒级。
// 曾经 mentionsColumn 每次调用现编一个正则,items×rules 次下来,一条 0 行的 SELECT
// 在网关里卡了 8 秒(相位里记成"读取")。
func TestSensitiveMaskTargetsWideSelectIsFast(t *testing.T) {
	sql := "SELECT  " + realCols + "  FROM t_adjust_event2_20260929 WHERE (network_name = 'Organic' AND customer_id = '2104929090334027776' AND src_reftag <> '') ORDER BY created_at DESC LIMIT 1;"
	var cols []string
	for _, c := range strings.Split(realCols, ",") {
		if i := strings.Index(c, " AS "); i >= 0 {
			c = c[i+4:]
		}
		cols = append(cols, c)
	}
	var rules []SensitiveRule
	for _, c := range []string{"ip_address", "idfa", "android_id", "oaid", "customer_id", "user_agent"} {
		rules = append(rules, SensitiveRule{Column: c})
	}
	for i := 0; len(rules) < 200; i++ {
		rules = append(rules, SensitiveRule{Column: fmt.Sprintf("secret_col_%d", i)})
	}
	t0 := time.Now()
	got := SensitiveMaskTargets(sql, cols, rules)
	if d := time.Since(t0); d > 300*time.Millisecond {
		t.Fatalf("175 列 × 200 条规则算打码目标用了 %v,应在毫秒级", d)
	}
	if len(got) == 0 {
		t.Fatalf("应命中 ip_address / idfa 等脱敏列,实际无打码目标")
	}
}

// 查表版必须与"逐条规则调 mentionsColumn"的朴素判定结论完全一致:这是换实现的前提。
// naiveMaskTargets 就是换之前的算法,留在这里当参照。
func naiveMaskTargets(sql string, cols []string, rules []SensitiveRule) []int {
	hit := map[int]bool{}
	for _, reading := range sqlReadings(sql) {
		stripped := StripComments(reading)
		tables := referencedTables(blankStringLiterals(stripped))
		var active []SensitiveRule
		for _, r := range rules {
			t := strings.ToLower(strings.TrimSpace(r.Table))
			if t == "" || t == "*" || tables[t] {
				active = append(active, r)
			}
		}
		for i, c := range cols {
			for _, r := range active {
				if strings.ToLower(strings.TrimSpace(c)) == strings.ToLower(strings.TrimSpace(r.Column)) {
					hit[i] = true
				}
			}
		}
		idents := blankStringLiterals(stripped)
		if spans, ok := selectListSpans(blankQuoted(stripped)); ok && len(spans) == len(cols) {
			for i, sp := range spans {
				for _, r := range active {
					if mentionsColumn(idents[sp[0]:sp[1]], r.Column) {
						hit[i] = true
					}
				}
			}
		}
	}
	var out []int
	for i := range cols {
		if hit[i] {
			out = append(out, i)
		}
	}
	return out
}

func TestSensitiveIndexMatchesNaive(t *testing.T) {
	rules := []SensitiveRule{
		{Column: "id_card"}, {Column: " Phone "}, {Table: "*", Column: "身份证"},
		{Table: "t_user", Column: "email"}, {Table: "T_Order", Column: "addr"},
		{Column: "first name"}, {Column: "a.b"}, {Column: "pwd$"}, {Column: "#tmp"},
		{Table: "other", Column: "salary"}, {Column: ""}, {Column: "ID_CARD", Style: MaskHash},
	}
	cases := []struct {
		sql  string
		cols []string
	}{
		{"SELECT id, id_card, phone FROM t_user", []string{"id", "id_card", "phone"}},
		{"SELECT id_card_no, xid_card, id_card2 FROM t", []string{"id_card_no", "xid_card", "id_card2"}},
		{"SELECT SUBSTR(ID_CARD,1,6) AS x, upper(Phone) y FROM t", []string{"x", "y"}},
		{"SELECT 身份证 AS a, 身份证号 AS b, x身份证 c FROM 用户", []string{"a", "b", "c"}},
		{"SELECT email, salary FROM t_user", []string{"email", "salary"}},
		{"SELECT email, salary FROM t_order", []string{"email", "salary"}},
		{"SELECT addr FROM `t_order`", []string{"addr"}},
		{"SELECT addr FROM t_order", []string{"addr"}},
		{"SELECT \"first name\" AS n, a.b AS m, t.a.b AS k FROM t", []string{"n", "m", "k"}},
		{"SELECT pwd$ AS p, pwd AS q, #tmp AS r FROM t", []string{"p", "q", "r"}},
		{"SELECT 'id_card' AS lit, concat(id_card, phone) AS c FROM t", []string{"lit", "c"}},
		{"SELECT * FROM t_user", []string{"id", "ID_CARD", "Email", "salary"}},
		{"SELECT a, b FROM t_user /* id_card */", []string{"a", "b"}},
		{"SELECT x FROM (SELECT id_card AS x FROM t_user) s", []string{"x"}},
		{"SELECT a FROM t WHERE x = 'it\\'s' UNION SELECT phone FROM t_user", []string{"a"}},
		{"SELECT id, \xff\xfeid_card FROM t", []string{"id", "c"}},
		{"SELECT " + realCols + " FROM t_user", nil},
	}
	for _, c := range cases {
		cols := c.cols
		if cols == nil {
			for _, s := range strings.Split(realCols, ",") {
				if i := strings.Index(s, " AS "); i >= 0 {
					s = s[i+4:]
				}
				cols = append(cols, s)
			}
		}
		got := SensitiveMaskTargets(c.sql, cols, rules)
		want := naiveMaskTargets(c.sql, cols, rules)
		if fmt.Sprint(got) != fmt.Sprint(want) && !(len(got) == 0 && len(want) == 0) {
			t.Errorf("%s\n  查表版 %v,朴素判定 %v", c.sql, got, want)
		}
	}
	ix := newSensitiveIndex(rules)
	if s := ix.styleFor("id_card"); s != MaskHash {
		t.Errorf("id_card 样式应取第一条写了样式的规则(hash),得到 %q", s)
	}
	if s := ix.styleFor("phone"); s != MaskPartial {
		t.Errorf("没配样式的字段应为 partial,得到 %q", s)
	}
}

// 规则条数对单次判定耗时的影响:go test -bench MaskTargets ./internal/gateway/
func benchMaskInput(n int) (string, []string, []SensitiveRule) {
	sql := "SELECT  " + realCols + "  FROM t_adjust_event2_20260929 WHERE customer_id = '1' LIMIT 1"
	var cols []string
	for _, c := range strings.Split(realCols, ",") {
		if i := strings.Index(c, " AS "); i >= 0 {
			c = c[i+4:]
		}
		cols = append(cols, c)
	}
	rules := []SensitiveRule{{Column: "ip_address"}, {Column: "idfa"}, {Table: "t_adjust_event2_20260929", Column: "customer_id"}}
	for i := 0; len(rules) < n; i++ {
		rules = append(rules, SensitiveRule{Column: fmt.Sprintf("secret_col_%d", i)})
	}
	return sql, cols, rules
}

func BenchmarkMaskTargets75(b *testing.B)   { benchMaskTargets(b, 75) }
func BenchmarkMaskTargets5000(b *testing.B) { benchMaskTargets(b, 5000) }

func benchMaskTargets(b *testing.B, n int) {
	sql, cols, rules := benchMaskInput(n)
	for i := 0; i < b.N; i++ {
		SensitiveMaskTargets(sql, cols, rules)
	}
}
