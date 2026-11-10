// 本文件负责加载本地 .env，同时保持进程环境变量的最高配置优先级。
//
// @author Andersen
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// defaultEnvFile 是未显式指定 ACME_ENV_FILE 时尝试加载的配置文件名。
const defaultEnvFile = ".env"

// HealthcheckListenAddress 只读取管理面监听地址，遵循进程环境优先于环境文件的规则。
// 健康检查不加载或校验主密钥、密码及其他与就绪探测无关的配置。
func HealthcheckListenAddress() (string, error) {
	value, exists := os.LookupEnv("ACME_LISTEN_ADDRESS")
	if !exists {
		var err error
		value, err = readEnvFileListenAddress()
		if err != nil {
			return "", err
		}
	}
	if value = strings.TrimSpace(value); value == "" {
		return defaultListenAddress, nil
	}
	return value, nil
}

// readEnvFileListenAddress 读取环境文件中首个监听地址，不解析或导出其他配置值。
func readEnvFileListenAddress() (string, error) {
	path := strings.TrimSpace(os.Getenv("ACME_ENV_FILE"))
	explicit := path != ""
	if !explicit {
		path = defaultEnvFile
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取环境配置文件 %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		name, rawValue, found := strings.Cut(line, "=")
		if strings.TrimSpace(name) != "ACME_LISTEN_ADDRESS" {
			continue
		}
		if !found {
			return "", fmt.Errorf("环境配置文件 %s 第 %d 行格式无效", path, lineNumber)
		}
		value, err := parseEnvValue(strings.TrimSpace(rawValue))
		if err != nil {
			return "", fmt.Errorf("环境配置文件 %s 第 %d 行格式无效: %w", path, lineNumber, err)
		}
		return value, nil
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("读取环境配置文件 %s: %w", path, err)
	}
	return "", nil
}

// loadEnvFile 加载 ACME_ENV_FILE 指定的文件；未指定时尝试当前目录下的 .env。
func loadEnvFile() error {
	path := strings.TrimSpace(os.Getenv("ACME_ENV_FILE"))
	explicit := path != ""
	if !explicit {
		path = defaultEnvFile
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) && !explicit {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取环境配置文件 %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "\ufeff"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		name, rawValue, found := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !found || !validEnvName(name) {
			return fmt.Errorf("环境配置文件 %s 第 %d 行格式无效", path, lineNumber)
		}
		value, err := parseEnvValue(strings.TrimSpace(rawValue))
		if err != nil {
			return fmt.Errorf("环境配置文件 %s 第 %d 行格式无效: %w", path, lineNumber, err)
		}
		if _, exists := os.LookupEnv(name); exists {
			continue
		}
		if err := os.Setenv(name, value); err != nil {
			return fmt.Errorf("加载环境变量 %s: %w", name, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取环境配置文件 %s: %w", path, err)
	}
	return nil
}

// validEnvName 判断名称是否符合可移植环境变量命名规则。
func validEnvName(value string) bool {
	if value == "" {
		return false
	}
	for index, char := range value {
		if char == '_' || char >= 'A' && char <= 'Z' || char >= 'a' && char <= 'z' || index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

// parseEnvValue 解析支持单双引号、转义和行尾注释的环境变量值。
func parseEnvValue(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if value[0] == '\'' {
		end := strings.Index(value[1:], "'")
		if end < 0 {
			return "", errors.New("单引号未闭合")
		}
		end++
		if !validEnvValueSuffix(value[end+1:]) {
			return "", errors.New("引号结束后存在无效内容")
		}
		return value[1:end], nil
	}
	if value[0] == '"' {
		end := quotedValueEnd(value)
		if end < 0 {
			return "", errors.New("双引号未闭合")
		}
		if !validEnvValueSuffix(value[end+1:]) {
			return "", errors.New("引号结束后存在无效内容")
		}
		parsed, err := strconv.Unquote(value[:end+1])
		if err != nil {
			return "", err
		}
		return parsed, nil
	}
	for index, char := range value {
		if char == '#' && index > 0 && (value[index-1] == ' ' || value[index-1] == '\t') {
			return strings.TrimSpace(value[:index]), nil
		}
	}
	return strings.TrimSpace(value), nil
}

// quotedValueEnd 返回双引号值的闭合位置，未闭合时返回 -1。
func quotedValueEnd(value string) int {
	escaped := false
	for index := 1; index < len(value); index++ {
		if escaped {
			escaped = false
			continue
		}
		if value[index] == '\\' {
			escaped = true
			continue
		}
		if value[index] == '"' {
			return index
		}
	}
	return -1
}

// validEnvValueSuffix 判断引号后的剩余内容是否为空或仅包含注释。
func validEnvValueSuffix(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || strings.HasPrefix(value, "#")
}
