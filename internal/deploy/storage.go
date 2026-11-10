// Package deploy 负责证书私钥加密存储及标准 PEM 目录的原子物化。
//
// @author Andersen
package deploy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/cryptoutil"
)

// aliasPattern 将业务别名限制为安全的单个目录名。
var aliasPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// fingerprintPattern 确保证书版本目录只能使用完整 SHA-256 十六进制指纹。
var fingerprintPattern = regexp.MustCompile(`^[A-Fa-f0-9]{64}$`)

// Bundle 是签发、传输和部署过程使用的内存证书材料。
type Bundle struct {
	Certificate  []byte    `json:"certificate"`  // Certificate 是叶证书 PEM。
	FullChain    []byte    `json:"fullChain"`    // FullChain 是叶证书和中间证书链 PEM。
	PrivateKey   []byte    `json:"privateKey"`   // PrivateKey 是匹配叶证书的私钥 PEM。
	Fingerprint  string    `json:"fingerprint"`  // Fingerprint 是叶证书 SHA-256 指纹。
	CommonName   string    `json:"commonName"`   // CommonName 是证书主名称。
	SANs         []string  `json:"sans"`         // SANs 是证书覆盖的 DNS 名称。
	Issuer       string    `json:"issuer"`       // Issuer 是证书签发者名称。
	SerialNumber string    `json:"serialNumber"` // SerialNumber 是证书序列号。
	NotBefore    time.Time `json:"notBefore"`    // NotBefore 是证书生效时间。
	NotAfter     time.Time `json:"notAfter"`     // NotAfter 是证书过期时间。
}

// Storage 保存控制平面证书材料，私钥始终以用途绑定密文落盘。
type Storage struct {
	root  string            // root 是加密证书库存储根目录。
	vault *cryptoutil.Vault // vault 加解密证书私钥。
}

// NewStorage 创建以数据目录下 certificates 为根的证书存储器。
func NewStorage(dataDir string, vault *cryptoutil.Vault) *Storage {
	return &Storage{root: filepath.Join(dataDir, "certificates"), vault: vault}
}

// Save 以指纹作为不可变版本目录保存材料，返回相对证书库的路径，便于异目录恢复。
func (s *Storage) Save(bundle Bundle) (certPath, chainPath, keyPath string, err error) {
	if !fingerprintPattern.MatchString(bundle.Fingerprint) {
		return "", "", "", errors.New("证书指纹必须是 SHA-256 十六进制字符串")
	}
	directory := filepath.Join(s.root, safeCommonName(bundle.CommonName), bundle.Fingerprint)
	if err = os.MkdirAll(directory, 0o700); err != nil {
		return "", "", "", err
	}
	certPath = filepath.Join(directory, "certificate.pem")
	chainPath = filepath.Join(directory, "fullchain.pem")
	keyPath = filepath.Join(directory, "private-key.pem.enc")
	encrypted, err := s.vault.Encrypt("certificate-key:"+bundle.Fingerprint, bundle.PrivateKey)
	if err != nil {
		return "", "", "", err
	}
	if err = atomicWrite(certPath, bundle.Certificate, 0o600); err != nil {
		return "", "", "", err
	}
	if err = atomicWrite(chainPath, bundle.FullChain, 0o600); err != nil {
		return "", "", "", err
	}
	if err = atomicWrite(keyPath, []byte(encrypted), 0o600); err != nil {
		return "", "", "", err
	}
	relativeDirectory := filepath.Join(safeCommonName(bundle.CommonName), bundle.Fingerprint)
	return filepath.ToSlash(filepath.Join(relativeDirectory, "certificate.pem")), filepath.ToSlash(filepath.Join(relativeDirectory, "fullchain.pem")), filepath.ToSlash(filepath.Join(relativeDirectory, "private-key.pem.enc")), nil
}

// Load 在当前证书库内解析新相对路径或旧绝对路径，仅在内存中解密私钥。
func (s *Storage) Load(certPath, chainPath, keyPath, fingerprint, commonName string) (Bundle, error) {
	certPath, err := materialPath(certPath, commonName, fingerprint, "certificate.pem")
	if err != nil {
		return Bundle{}, err
	}
	chainPath, err = materialPath(chainPath, commonName, fingerprint, "fullchain.pem")
	if err != nil {
		return Bundle{}, err
	}
	keyPath, err = materialPath(keyPath, commonName, fingerprint, "private-key.pem.enc")
	if err != nil {
		return Bundle{}, err
	}
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return Bundle{}, err
	}
	defer root.Close()
	certificate, err := root.ReadFile(certPath)
	if err != nil {
		return Bundle{}, err
	}
	chain, err := root.ReadFile(chainPath)
	if err != nil {
		return Bundle{}, err
	}
	envelope, err := root.ReadFile(keyPath)
	if err != nil {
		return Bundle{}, err
	}
	privateKey, err := s.vault.Decrypt("certificate-key:"+fingerprint, string(envelope))
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{Certificate: certificate, FullChain: chain, PrivateKey: privateKey, Fingerprint: fingerprint, CommonName: commonName}, nil
}

// Materialize 将证书写入不可变版本目录并原子切换 current 符号链接。
func Materialize(root, targetAlias string, certificateID int64, bundle Bundle) (string, error) {
	if !aliasPattern.MatchString(targetAlias) {
		return "", errors.New("目录别名只能包含小写字母、数字、下划线和中划线")
	}
	if !fingerprintPattern.MatchString(bundle.Fingerprint) {
		return "", errors.New("证书指纹必须是 SHA-256 十六进制字符串")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if err := ensureSafeRoot(absRoot); err != nil {
		return "", err
	}
	canonicalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return "", err
	}
	targetRoot := filepath.Join(canonicalRoot, targetAlias)
	versionDir := filepath.Join(targetRoot, "versions", bundle.Fingerprint)
	if err := ensureWithin(canonicalRoot, versionDir); err != nil {
		return "", err
	}
	if err := mkdirAllWithoutSymlinks(canonicalRoot, versionDir, 0o750); err != nil {
		return "", err
	}
	if err := ensurePathWithoutSymlinks(canonicalRoot, versionDir); err != nil {
		return "", err
	}
	if err := atomicWriteWithin(canonicalRoot, filepath.Join(versionDir, "certificate.pem"), bundle.Certificate, 0o644); err != nil {
		return "", err
	}
	if err := atomicWriteWithin(canonicalRoot, filepath.Join(versionDir, "fullchain.pem"), bundle.FullChain, 0o644); err != nil {
		return "", err
	}
	if err := atomicWriteWithin(canonicalRoot, filepath.Join(versionDir, "private-key.pem"), bundle.PrivateKey, 0o640); err != nil {
		return "", err
	}
	metadata, _ := json.MarshalIndent(map[string]any{
		"certificateId": certificateID,
		"commonName":    bundle.CommonName,
		"sans":          bundle.SANs,
		"fingerprint":   bundle.Fingerprint,
		"notAfter":      bundle.NotAfter,
		"deployedAt":    time.Now().UTC(),
	}, "", "  ")
	if err := atomicWriteWithin(canonicalRoot, filepath.Join(versionDir, "metadata.json"), append(metadata, '\n'), 0o640); err != nil {
		return "", err
	}
	if err := switchCurrent(canonicalRoot, targetRoot, filepath.Join("versions", bundle.Fingerprint)); err != nil {
		return "", err
	}
	if err := atomicWriteWithin(canonicalRoot, filepath.Join(targetRoot, "active.version"), []byte(bundle.Fingerprint+"\n"), 0o640); err != nil {
		return "", err
	}
	return filepath.Join(targetRoot, "current"), nil
}

// ValidateMaterialized 验证 current 指针和当前版本证书材料仍与期望状态完全一致。
func ValidateMaterialized(root, targetAlias string, domains []string, fingerprint string) error {
	if !aliasPattern.MatchString(targetAlias) || !fingerprintPattern.MatchString(fingerprint) {
		return errors.New("部署目标别名或证书指纹无效")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	canonicalRoot, err := filepath.EvalSymlinks(absRoot)
	if err != nil {
		return err
	}
	targetRoot := filepath.Join(canonicalRoot, targetAlias)
	versionDir := filepath.Join(targetRoot, "versions", fingerprint)
	if err := ensureWithin(canonicalRoot, versionDir); err != nil {
		return err
	}
	if err := ensurePathWithoutSymlinks(canonicalRoot, versionDir); err != nil {
		return err
	}
	activeVersion, err := os.ReadFile(filepath.Join(targetRoot, "active.version"))
	if err != nil || strings.TrimSpace(string(activeVersion)) != fingerprint {
		return errors.New("部署活动版本与期望指纹不一致")
	}
	currentPath := filepath.Join(targetRoot, "current")
	currentInfo, err := os.Lstat(currentPath)
	if err != nil || currentInfo.Mode()&os.ModeSymlink == 0 {
		return errors.New("部署 current 指针缺失或类型无效")
	}
	currentTarget, err := os.Readlink(currentPath)
	if err != nil || filepath.Clean(currentTarget) != filepath.Join("versions", fingerprint) {
		return errors.New("部署 current 指针与期望版本不一致")
	}
	certificate, err := os.ReadFile(filepath.Join(versionDir, "certificate.pem"))
	if err != nil {
		return err
	}
	fullChain, err := os.ReadFile(filepath.Join(versionDir, "fullchain.pem"))
	if err != nil || len(fullChain) == 0 {
		return errors.New("部署完整证书链缺失")
	}
	privateKey, err := os.ReadFile(filepath.Join(versionDir, "private-key.pem"))
	if err != nil {
		return err
	}
	return ValidateBundleDomains(Bundle{Certificate: certificate, FullChain: fullChain, PrivateKey: privateKey}, domains, fingerprint, true)
}

// PruneMaterializedVersions 删除超过保留期且不属于 current 或最近保底数量的部署版本。
func PruneMaterializedVersions(root string, before time.Time, keepHistorical int) (int, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return 0, err
	}
	canonicalRoot, err := filepath.EvalSymlinks(absRoot)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	targets, err := os.ReadDir(canonicalRoot)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, target := range targets {
		if !target.IsDir() || !aliasPattern.MatchString(target.Name()) {
			continue
		}
		targetRoot := filepath.Join(canonicalRoot, target.Name())
		versionsRoot := filepath.Join(targetRoot, "versions")
		if err := ensurePathWithoutSymlinks(canonicalRoot, versionsRoot); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return removed, err
		}
		currentTarget, _ := os.Readlink(filepath.Join(targetRoot, "current"))
		currentFingerprint := filepath.Base(filepath.Clean(currentTarget))
		versions, err := os.ReadDir(versionsRoot)
		if err != nil {
			return removed, err
		}
		// 版本排序回调把最近修改的回退版本放在前面。
		sort.Slice(versions, func(left, right int) bool {
			leftInfo, leftErr := versions[left].Info()
			rightInfo, rightErr := versions[right].Info()
			if leftErr != nil || rightErr != nil {
				return versions[left].Name() > versions[right].Name()
			}
			return leftInfo.ModTime().After(rightInfo.ModTime())
		})
		historicalIndex := 0
		for _, version := range versions {
			if !version.IsDir() || !fingerprintPattern.MatchString(version.Name()) || version.Name() == currentFingerprint {
				continue
			}
			info, err := version.Info()
			if err != nil {
				return removed, err
			}
			historicalIndex++
			if historicalIndex <= keepHistorical || !info.ModTime().Before(before) {
				continue
			}
			versionPath := filepath.Join(versionsRoot, version.Name())
			if err := ensureWithin(canonicalRoot, versionPath); err != nil {
				return removed, err
			}
			if err := os.RemoveAll(versionPath); err != nil {
				return removed, err
			}
			removed++
		}
	}
	return removed, nil
}

// switchCurrent 通过同目录符号链接重命名原子切换 current 指向。
func switchCurrent(root, targetRoot, relativeTarget string) error {
	if err := mkdirAllWithoutSymlinks(root, targetRoot, 0o750); err != nil {
		return err
	}
	if err := ensurePathWithoutSymlinks(root, targetRoot); err != nil {
		return err
	}
	temporary := filepath.Join(targetRoot, ".current-"+fmt.Sprint(time.Now().UnixNano()))
	if err := os.Symlink(relativeTarget, temporary); err != nil {
		return err
	}
	current := filepath.Join(targetRoot, "current")
	if err := os.Rename(temporary, current); err != nil {
		if removeErr := os.Remove(current); removeErr != nil && !os.IsNotExist(removeErr) {
			_ = os.Remove(temporary)
			return removeErr
		}
		if retryErr := os.Rename(temporary, current); retryErr != nil {
			_ = os.Remove(temporary)
			return retryErr
		}
	}
	return syncDirectory(targetRoot)
}

// atomicWriteWithin 在确认路径未逃逸且不含符号链接后执行原子写入。
func atomicWriteWithin(root, path string, content []byte, mode os.FileMode) error {
	if err := ensureWithin(root, path); err != nil {
		return err
	}
	if err := ensurePathWithoutSymlinks(root, filepath.Dir(path)); err != nil {
		return err
	}
	return atomicWrite(path, content, mode)
}

// atomicWrite 写入、同步并重命名临时文件，随后同步父目录。
func atomicWrite(path string, content []byte, mode os.FileMode) error {
	if err := rejectExistingSymlink(path); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".tmp-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return syncDirectory(directory)
}

// syncDirectory 同步目录元数据以提高重命名结果的崩溃一致性。
func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// ensureSafeRoot 拒绝过宽根路径并创建部署根目录。
func ensureSafeRoot(root string) error {
	if root == string(filepath.Separator) || root == "." || root == "" {
		return errors.New("拒绝使用不安全的部署根目录")
	}
	return os.MkdirAll(root, 0o750)
}

// ensureWithin 确认子路径经清理后仍位于指定根目录内。
func ensureWithin(root, child string) error {
	relative, err := filepath.Rel(root, child)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("部署路径超出根目录")
	}
	return nil
}

// mkdirAllWithoutSymlinks 逐级创建目录，并拒绝任何已有符号链接或非目录组件。
func mkdirAllWithoutSymlinks(root, child string, mode os.FileMode) error {
	if err := ensureWithin(root, child); err != nil {
		return err
	}
	relative, err := filepath.Rel(root, child)
	if err != nil {
		return err
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			if err := os.Mkdir(current, mode); err != nil && !os.IsExist(err) {
				return err
			}
			info, err = os.Lstat(current)
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("部署路径包含符号链接或非目录组件")
		}
	}
	return nil
}

// ensurePathWithoutSymlinks 复查既有路径的每一级组件都是真实目录。
func ensurePathWithoutSymlinks(root, child string) error {
	if err := ensureWithin(root, child); err != nil {
		return err
	}
	relative, err := filepath.Rel(root, child)
	if err != nil {
		return err
	}
	current := root
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("部署路径包含符号链接或非目录组件")
		}
	}
	return nil
}

// rejectExistingSymlink 拒绝用原子写入覆盖攻击者预置的符号链接。
func rejectExistingSymlink(path string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("目标文件不能是符号链接")
	}
	return nil
}

// safeCommonName 将证书主名称转换为不含路径语义的缓存目录名。
func safeCommonName(value string) string {
	replacer := strings.NewReplacer("*", "wildcard", "/", "_", "\\", "_", "..", "_")
	return replacer.Replace(strings.ToLower(strings.TrimSpace(value)))
}
