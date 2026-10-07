// Package middleware M0 轻量鉴权: 自实现 HS256 JWT(仅标准库), 用于演示身份传递。
// 生产替换为真实 KYC+JWT 签发体系(可接入微信 openid 映射)。
package middleware

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var secret = []byte("hiveget-m0-dev-secret-replace-in-prod")

// Claims JWT 载荷
type Claims struct {
	UserID string `json:"uid"`
	Role   string `json:"role"`
	OrgID  string `json:"org_id,omitempty"` // 组织开放 API 凭证映射的组织(空=个人)
	Exp    int64  `json:"exp"`
}

// Sign 签发 token
func Sign(userID, role string) (string, error) {
	claims := Claims{UserID: userID, Role: role, Exp: time.Now().Add(24 * time.Hour).Unix()}
	header, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	seg := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	signing := seg(header) + "." + seg(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signing))
	sig := seg(mac.Sum(nil))
	return signing + "." + sig, nil
}

// Parse 校验并返回 Claims
func Parse(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid token format")
	}
	signing := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signing))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return nil, errors.New("invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, err
	}
	if c.Exp < time.Now().Unix() {
		return nil, errors.New("token expired")
	}
	return &c, nil
}

// ExtractToken 从 Authorization: Bearer <token> 提取
func ExtractToken(header string) (string, bool) {
	if !strings.HasPrefix(header, "Bearer ") {
		return "", false
	}
	t := strings.TrimPrefix(header, "Bearer ")
	if t == "" {
		return "", false
	}
	return t, true
}
