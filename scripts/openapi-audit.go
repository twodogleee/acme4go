//go:build openapiaudit

// Command openapi-audit 比较 HTTP 路由注册与 OpenAPI 路径，阻止接口契约再次漂移。
//
// @author Andersen
package main

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

// routePattern 提取 Go 路由注册中的方法和路径。
var routePattern = regexp.MustCompile(`Handle(?:Func)?\("(GET|POST|PUT|DELETE|PATCH) ([^" ]+)"`)

// pathParameterPattern 统一不同名称的 OpenAPI 路径参数。
var pathParameterPattern = regexp.MustCompile(`\{[^}]+\}`)

// main 加载实现和契约路由并报告任一方向的缺失项。
func main() {
	implementation, err := routesFromGo("internal/control/server.go")
	if err != nil {
		fail(err)
	}
	contract, err := routesFromOpenAPI("api/openapi.yaml")
	if err != nil {
		fail(err)
	}
	missingContract := difference(implementation, contract)
	missingImplementation := difference(contract, implementation)
	if len(missingContract) > 0 || len(missingImplementation) > 0 {
		fmt.Fprintf(os.Stderr, "OpenAPI 缺少实现路由: %v\n实现缺少契约路由: %v\n", missingContract, missingImplementation)
		os.Exit(1)
	}
	fmt.Printf("OpenAPI 路由审计通过：%d 个接口\n", len(contract))
}

// routesFromGo 从控制层源码提取带方法的显式 ServeMux 路由。
func routesFromGo(path string) (map[string]struct{}, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	result := make(map[string]struct{})
	for _, match := range routePattern.FindAllStringSubmatch(string(content), -1) {
		result[normalizeRoute(match[1]+" "+match[2])] = struct{}{}
	}
	return result, nil
}

// routesFromOpenAPI 从 YAML 的 paths 区块提取方法与路径，不依赖额外 YAML 工具。
func routesFromOpenAPI(path string) (map[string]struct{}, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	result := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	insidePaths := false
	currentPath := ""
	for scanner.Scan() {
		line := scanner.Text()
		if line == "paths:" {
			insidePaths = true
			continue
		}
		if insidePaths && line == "components:" {
			break
		}
		if !insidePaths {
			continue
		}
		if strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":") {
			currentPath = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		method := strings.TrimSuffix(strings.TrimSpace(line), ":")
		if currentPath != "" && strings.HasPrefix(line, "    ") && !strings.HasPrefix(line, "      ") && isHTTPMethod(method) {
			result[normalizeRoute(strings.ToUpper(method)+" "+currentPath)] = struct{}{}
		}
	}
	return result, scanner.Err()
}

// isHTTPMethod 判断 OpenAPI 路径子项是否为本项目支持的请求方法。
func isHTTPMethod(value string) bool {
	switch value {
	case "get", "post", "put", "delete", "patch":
		return true
	default:
		return false
	}
}

// normalizeRoute 把路径参数名称统一成占位符，只比较对外方法与路径结构。
func normalizeRoute(value string) string {
	return pathParameterPattern.ReplaceAllString(value, "{}")
}

// difference 返回只存在于左侧路由集合中的稳定排序结果。
func difference(left, right map[string]struct{}) []string {
	values := make([]string, 0)
	for route := range left {
		if _, exists := right[route]; !exists {
			values = append(values, route)
		}
	}
	sort.Strings(values)
	return values
}

// fail 输出启动级错误并以失败状态结束审计。
func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
