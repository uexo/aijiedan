// Package auth M0 轻量密码哈希: 仅标准库实现 PBKDF2-HMAC-SHA256。
// 不引入 bcrypt/argon2 等外部依赖, 保持零依赖编译原则。
// 存储串格式: pbkdf2_sha256$<iter>$<saltB64>$<hashB64>
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strconv"
	"strings"
)

const (
	pbkdf2Iter  = 4096
	pbkdf2KeyLen = 32
	prefix      = "pbkdf2_sha256"
)

// HashPassword 生成 salt+hash 存储串(PBKDF2-HMAC-SHA256, 4096 次迭代, 32 字节输出)。
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := pbkdf2([]byte(pw), salt, pbkdf2Iter, pbkdf2KeyLen)
	return format(pbkdf2Iter, salt, hash), nil
}

// VerifyPassword 校验明文密码与存储串是否匹配(恒定时间比较, 防时序侧信道)。
func VerifyPassword(stored, pw string) bool {
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != prefix {
		return false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) == 0 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil || len(want) == 0 {
		return false
	}
	computed := pbkdf2([]byte(pw), salt, iter, len(want))
	return subtle.ConstantTimeCompare(computed, want) == 1
}

func format(iter int, salt, hash []byte) string {
	return prefix + "$" +
		strconv.Itoa(iter) + "$" +
		base64.RawStdEncoding.EncodeToString(salt) + "$" +
		base64.RawStdEncoding.EncodeToString(hash)
}

// pbkdf2 纯标准库实现 PBKDF2-HMAC-SHA256(RFC 8018)。
func pbkdf2(password, salt []byte, iter, keyLen int) []byte {
	prf := hmac.New(sha256.New, password)
	hLen := sha256.New().Size() // 32
	blocks := (keyLen + hLen - 1) / hLen
	out := make([]byte, 0, blocks*hLen)
	ib := make([]byte, 4)
	for i := 1; i <= blocks; i++ {
		ib[0] = byte(i >> 24)
		ib[1] = byte(i >> 16)
		ib[2] = byte(i >> 8)
		ib[3] = byte(i)
		// U1 = PRF(password, salt || INT_32_BE(i))
		prf.Reset()
		prf.Write(salt)
		prf.Write(ib)
		u := prf.Sum(nil)
		t := make([]byte, hLen)
		copy(t, u)
		// U2..Un
		for j := 2; j <= iter; j++ {
			prf.Reset()
			prf.Write(u)
			u = prf.Sum(nil)
			for k := range t {
				t[k] ^= u[k]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}
