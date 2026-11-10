// 本文件实现 Server 数据目录的一致备份和离线诊断命令。
//
// @author Andersen
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/twodogleee/acme4go/internal/config"
	"github.com/twodogleee/acme4go/internal/cryptoutil"
	"github.com/twodogleee/acme4go/internal/deploy"
	"github.com/twodogleee/acme4go/internal/pki"
	"github.com/twodogleee/acme4go/internal/store"
)

// runBackup 创建 SQLite 一致快照，并把数据目录中的其余关键材料打包为权限 0600 的 tar.gz。
func runBackup(cfg config.Config, destination string) error {
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	if _, err := os.Stat(destination); err == nil {
		return errors.New("备份目标已存在，拒绝覆盖")
	} else if !os.IsNotExist(err) {
		return err
	}
	destinationDirectory := filepath.Dir(destination)
	if err := os.MkdirAll(destinationDirectory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(destinationDirectory, ".acme4go-backup-*.tar.gz")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	if err := temporary.Close(); err != nil {
		return err
	}
	defer os.Remove(temporaryPath)
	snapshotPath := temporaryPath + ".db"
	defer os.Remove(snapshotPath)
	database, err := store.Open(cfg.DataDir)
	if err != nil {
		return err
	}
	if err := database.Backup(context.Background(), snapshotPath); err != nil {
		database.Close()
		return err
	}
	if err := database.Close(); err != nil {
		return err
	}
	archive, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	compressor := gzip.NewWriter(archive)
	written := tar.NewWriter(compressor)
	if err := addBackupFile(written, snapshotPath, filepath.Join("data", "acme4go.db")); err != nil {
		archive.Close()
		return err
	}
	// 数据目录遍历回调排除在线 SQLite 文件和当前备份临时文件。
	err = filepath.WalkDir(cfg.DataDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == cfg.DataDir || entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("数据目录包含不允许备份的符号链接: %s", path)
		}
		name := entry.Name()
		if name == "acme4go.db" || name == "acme4go.db-wal" || name == "acme4go.db-shm" || path == destination || path == temporaryPath || path == snapshotPath {
			return nil
		}
		relative, err := filepath.Rel(cfg.DataDir, path)
		if err != nil {
			return err
		}
		return addBackupFile(written, path, filepath.Join("data", relative))
	})
	if closeErr := written.Close(); err == nil {
		err = closeErr
	}
	if closeErr := compressor.Close(); err == nil {
		err = closeErr
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	return nil
}

// addBackupFile 把单个普通文件按受限权限写入 tar 归档。
func addBackupFile(writer *tar.Writer, source, archiveName string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("备份源不是普通文件: %s", source)
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = filepath.ToSlash(archiveName)
	header.Mode = 0o600
	if err := writer.WriteHeader(header); err != nil {
		return err
	}
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(writer, file)
	return err
}

// runDoctor 只读检查现有 SQLite、内部 CA、关键密文及本机已成功部署目标，不创建身份材料。
func runDoctor(cfg config.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	database, err := store.OpenReadOnly(ctx, cfg.DataDir)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := database.IntegrityCheck(ctx); err != nil {
		return err
	}
	vault, err := cryptoutil.NewVault(cfg.MasterKey)
	if err != nil {
		return err
	}
	if err := pki.CheckExistingCA(cfg.DataDir, vault); err != nil {
		return fmt.Errorf("内部 CA 诊断失败: %w", err)
	}
	if err := checkCredentialEnvelopes(ctx, database, vault); err != nil {
		return err
	}
	storage := deploy.NewStorage(cfg.DataDir, vault)
	for page := 1; ; page++ {
		certificates, total, err := database.ListCertificatesPage(ctx, cfg.RenewBefore, page, 200)
		if err != nil {
			return err
		}
		for _, certificate := range certificates {
			bundle, err := storage.Load(certificate.CertPath, certificate.ChainPath, certificate.KeyPath, certificate.Fingerprint, certificate.CommonName)
			if err != nil {
				return fmt.Errorf("证书 #%d 加密材料不可读: %w", certificate.ID, err)
			}
			if len(bundle.Certificate) == 0 || len(bundle.FullChain) == 0 || len(bundle.PrivateKey) == 0 {
				return fmt.Errorf("证书 #%d 材料不完整", certificate.ID)
			}
		}
		if int64(page*200) >= total {
			break
		}
	}
	if cfg.LocalDeploymentEnabled {
		targets, err := database.ListTargets(ctx)
		if err != nil {
			return err
		}
		for _, target := range targets {
			if target.ExecutorType != store.ExecutorLocal || target.Status != "SUCCESS" || target.LastFingerprint == nil {
				continue
			}
			if err := deploy.ValidateMaterialized(cfg.DeploymentDir, target.TargetAlias, target.SeriesDomains, *target.LastFingerprint); err != nil {
				return fmt.Errorf("本机部署目标 #%d 实际状态异常: %w", target.ID, err)
			}
		}
	}
	fmt.Printf("诊断通过：SQLite、内部 CA、DNS/ACME 密文、证书加密材料和本机部署状态正常（%s）\n", strings.TrimSpace(cfg.DataDir))
	return nil
}
