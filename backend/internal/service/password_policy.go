package service

import (
	"strings"

	"velagateway/internal/apimsg"
)

// 口令规则 —— 建号(CreateUser)、管理员重置(AdminSetPassword)、本人修改(ChangeOwnPassword)
// 同一条,前端校验也照这条。
//
// 从前只要求 8 位(2026-10 安全审计 M7):12345678、password、qwerty123 都能过,而登录频控只按
// 来源计数,换着地址猜这几条常见口令就够了。现在另拒常见弱口令与单一字符重复。黑名单只收最常见
// 的那一小撮 —— 目的不是穷举,是把"第一批会被试的"挡在外面。

var ErrPasswordTooCommon = apimsg.Errorf("%w: 密码过于常见,请换一个", ErrBadRequest)

var commonPasswords = func() map[string]bool {
	m := map[string]bool{}
	for _, p := range []string{
		"12345678", "123456789", "1234567890", "87654321", "11111111", "00000000", "88888888",
		"12341234", "11223344", "123123123", "1qaz2wsx", "1q2w3e4r", "1q2w3e4r5t", "zaq12wsx",
		"password", "password1", "password123", "passw0rd", "p@ssw0rd", "p@ssword", "admin123",
		"admin1234", "administrator", "root1234", "qwerty123", "qwertyui", "qwerty12", "qwer1234",
		"asdf1234", "asdfghjk", "abc12345", "abcd1234", "a1234567", "aa123456", "iloveyou",
		"welcome1", "welcome123", "letmein1", "changeme", "default1", "test1234", "11111111a",
		"woaini1314", "5201314520", "abc123456", "qq123456", "a123456789",
	} {
		m[p] = true
	}
	return m
}()

// checkPasswordPolicy 校验一条新口令:本人修改那一路用(ErrBadRequest 包装)。
func checkPasswordPolicy(pw string) error {
	if len(pw) < minPasswordLen {
		return ErrPasswordTooShort
	}
	if isCommonPassword(pw) {
		return ErrPasswordTooCommon
	}
	return nil
}

// passwordPolicyInput 同一条规则,给管理端(建号 / 重置)用:那两个处理器只把 InputError 的原因透给前端。
func passwordPolicyInput(pw string) error {
	if len(pw) < minPasswordLen {
		return invalidf("密码至少 %d 位", minPasswordLen)
	}
	if isCommonPassword(pw) {
		return invalidf("密码过于常见,请换一个")
	}
	return nil
}

func isCommonPassword(pw string) bool {
	return commonPasswords[strings.ToLower(pw)] || strings.Count(pw, pw[:1]) == len(pw)
}
