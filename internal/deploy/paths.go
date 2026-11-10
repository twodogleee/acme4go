// 本文件解析证书材料标识，兼容旧绝对路径并将所有读取限制在当前证书库内。
//
// @author Andersen
package deploy

import (
	"errors"
	"path/filepath"
	"strings"
)

// materialPath 只接受匹配证书身份和文件类型的规范路径；旧目录仅作格式识别，从不作为读取回退。
func materialPath(value, commonName, fingerprint, filename string) (string, error) {
	value = filepath.FromSlash(value)
	if !fingerprintPattern.MatchString(fingerprint) || safeCommonName(commonName) == "" {
		return "", errors.New("证书材料身份无效")
	}
	want := filepath.Join(safeCommonName(commonName), fingerprint, filename)
	if !filepath.IsLocal(want) || filepath.Clean(value) != value {
		return "", errors.New("证书材料路径不规范")
	}
	if value == want {
		return want, nil
	}
	legacySuffix := string(filepath.Separator) + filepath.Join("certificates", want)
	if filepath.IsAbs(value) && strings.HasSuffix(value, legacySuffix) {
		return want, nil
	}
	return "", errors.New("证书材料路径与当前证书身份不匹配")
}
