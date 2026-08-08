package proxy

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type BackupComponent struct {
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Path   string `json:"path,omitempty"`
	KeyID  string `json:"key_id,omitempty"`
}

type BackupCheckResult struct {
	OK        bool            `json:"ok"`
	CheckedAt time.Time       `json:"checked_at"`
	SQLite    BackupComponent `json:"sqlite"`
	Secrets   BackupComponent `json:"secrets"`
	Issues    []string        `json:"issues"`
}

func (s *Server) backupCheck() BackupCheckResult {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()

	result := BackupCheckResult{OK: true, CheckedAt: time.Now().UTC(), Issues: []string{}}
	if s.config.DB == nil {
		result.OK = false
		result.SQLite = BackupComponent{Detail: "SQLite 持久化未启用"}
		result.Issues = append(result.Issues, "SQLite 持久化未启用，无法生成一致性备份")
	} else if err := s.config.DB.Ping(); err != nil {
		result.OK = false
		result.SQLite = BackupComponent{Detail: "SQLite 连接失败"}
		result.Issues = append(result.Issues, "SQLite 连接失败："+err.Error())
	} else {
		var tableCount int
		err := s.config.DB.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('upstream_accounts', 'virtual_keys', 'usage_events', 'alerts', 'audit_logs')`).Scan(&tableCount)
		if err != nil || tableCount < 5 {
			result.OK = false
			result.SQLite = BackupComponent{Detail: "SQLite 结构不完整"}
			result.Issues = append(result.Issues, "SQLite 缺少必要业务表")
		} else {
			result.SQLite = BackupComponent{OK: true, Detail: "SQLite 连接和业务表正常"}
		}
	}

	cipher := s.config.SecretCipher
	if cipher == nil {
		if s.config.DB == nil {
			result.Secrets = BackupComponent{OK: true, Detail: "未启用凭据加密"}
			return result
		}
		encrypted, err := hasEncryptedSecrets(s.config.DB)
		if err != nil {
			result.OK = false
			result.Secrets = BackupComponent{Detail: "无法检查加密凭据"}
			result.Issues = append(result.Issues, "无法检查 SQLite 中的凭据状态："+err.Error())
		} else if encrypted {
			result.OK = false
			result.Secrets = BackupComponent{Detail: "凭据已加密但主密钥未配置"}
			result.Issues = append(result.Issues, "SQLite 中存在加密凭据，但服务未配置主密钥")
		} else {
			result.Secrets = BackupComponent{OK: true, Detail: "未启用凭据加密"}
		}
	} else if path := cipher.FilePath(); path != "" {
		diskCipher, err := OpenSecretCipher(s.config.DB, path)
		if err != nil {
			result.OK = false
			result.Secrets = BackupComponent{Detail: "本地主密钥文件无法读取", Path: path, KeyID: cipher.CurrentID()}
			result.Issues = append(result.Issues, "无法读取本地主密钥文件："+err.Error())
		} else if diskCipher.CurrentID() != cipher.CurrentID() {
			result.OK = false
			result.Secrets = BackupComponent{Detail: "磁盘主密钥与当前服务不匹配", Path: path, KeyID: diskCipher.CurrentID()}
			result.Issues = append(result.Issues, "磁盘主密钥与当前服务使用的主密钥不一致")
		} else if err := ValidateStoredSecrets(s.config.DB, diskCipher); err != nil {
			result.OK = false
			result.Secrets = BackupComponent{Detail: "凭据无法使用磁盘主密钥解密", Path: path, KeyID: diskCipher.CurrentID()}
			result.Issues = append(result.Issues, "凭据与磁盘主密钥不匹配："+err.Error())
		} else {
			result.Secrets = BackupComponent{OK: true, Detail: "本地主密钥文件和凭据匹配", Path: path, KeyID: diskCipher.CurrentID()}
		}
	} else if err := ValidateStoredSecrets(s.config.DB, cipher); err != nil {
		result.OK = false
		result.Secrets = BackupComponent{Detail: "凭据无法使用当前主密钥解密", KeyID: cipher.CurrentID()}
		result.Issues = append(result.Issues, "凭据与当前主密钥不匹配："+err.Error())
	} else {
		result.Secrets = BackupComponent{OK: true, Detail: "外部主密钥已配置，恢复时需提供 SECRETS_MASTER_KEY", KeyID: cipher.CurrentID()}
	}
	return result
}

func (s *Server) handleBackups(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/admin/backups/check":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		result := s.backupCheck()
		writeJSON(w, http.StatusOK, result)
	case "/admin/backups/download":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", "GET")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if s.config.DB == nil {
			writeJSON(w, http.StatusConflict, map[string]any{"error": "SQLite persistence is not enabled"})
			return
		}
		path, err := s.createBackup(r.Context())
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "create backup failed"})
			return
		}
		defer os.Remove(path)
		file, err := os.Open(path)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "open backup failed"})
			return
		}
		defer file.Close()
		if s.audit != nil {
			s.audit.Record("backup_downloaded", "backup", "", "下载 SQLite 与密钥备份", map[string]any{"key_id": s.config.SecretCipher.CurrentID()}, time.Now())
		}
		filename := "seekops-backup-" + time.Now().Format("20060102-150405") + ".zip"
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		http.ServeContent(w, r, filename, time.Now(), file)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) createBackup(ctx context.Context) (string, error) {
	s.backupMu.Lock()
	defer s.backupMu.Unlock()

	workDir, err := os.MkdirTemp("", "seekops-backup-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(workDir)
	databasePath := filepath.Join(workDir, "seekops.db")
	if _, err := s.config.DB.ExecContext(ctx, `VACUUM INTO ?`, databasePath); err != nil {
		return "", fmt.Errorf("vacuum sqlite: %w", err)
	}
	archivePath := filepath.Join(workDir, "seekops-backup.zip")
	archiveFile, err := os.Create(archivePath)
	if err != nil {
		return "", err
	}
	zipWriter := zip.NewWriter(archiveFile)
	manifest := map[string]any{
		"format_version":               1,
		"created_at":                   time.Now().UTC(),
		"database":                     "seekops.db",
		"key_storage":                  "disabled",
		"external_master_key_required": false,
	}
	if cipher := s.config.SecretCipher; cipher != nil {
		manifest["key_storage"] = "external"
		manifest["master_key_id"] = cipher.CurrentID()
		if keyPath := cipher.FilePath(); keyPath != "" {
			manifest["key_storage"] = "local_file"
			manifest["key_file"] = "seekops.key"
			if _, readErr := os.Stat(keyPath); readErr == nil {
				if err := writeZipFile(zipWriter, "seekops.key", keyPath); err != nil {
					zipWriter.Close()
					archiveFile.Close()
					return "", err
				}
			} else {
				zipWriter.Close()
				archiveFile.Close()
				return "", fmt.Errorf("read master key: %w", readErr)
			}
		} else {
			manifest["external_master_key_required"] = true
		}
	}
	manifestData, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		zipWriter.Close()
		archiveFile.Close()
		return "", err
	}
	if err := writeZipBytes(zipWriter, "manifest.json", append(manifestData, '\n')); err != nil {
		zipWriter.Close()
		archiveFile.Close()
		return "", err
	}
	if err := writeZipFile(zipWriter, "seekops.db", databasePath); err != nil {
		zipWriter.Close()
		archiveFile.Close()
		return "", err
	}
	if err := zipWriter.Close(); err != nil {
		archiveFile.Close()
		return "", err
	}
	if err := archiveFile.Close(); err != nil {
		return "", err
	}
	result, err := os.CreateTemp("", "seekops-download-*.zip")
	if err != nil {
		return "", err
	}
	resultPath := result.Name()
	if err := result.Chmod(0o600); err != nil {
		result.Close()
		os.Remove(resultPath)
		return "", err
	}
	if err := result.Close(); err != nil {
		os.Remove(resultPath)
		return "", err
	}
	if err := copyFile(archivePath, resultPath); err != nil {
		os.Remove(resultPath)
		return "", err
	}
	return resultPath, nil
}

func writeZipBytes(writer *zip.Writer, name string, data []byte) error {
	entry, err := writer.Create(name)
	if err != nil {
		return err
	}
	_, err = entry.Write(data)
	return err
}

func writeZipFile(writer *zip.Writer, name, path string) error {
	entry, err := writer.Create(name)
	if err != nil {
		return err
	}
	input, err := os.Open(path)
	if err != nil {
		return err
	}
	defer input.Close()
	_, err = io.Copy(entry, input)
	return err
}

func copyFile(source, target string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return err
	}
	return output.Close()
}
