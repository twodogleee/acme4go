// Command comment-audit 检查 Go 源码中的类型、字段、常量、函数和回调注释。
//
// @author Andersen
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

// auditState 保存单次 Go 注释审计的文件上下文和缺失计数。
type auditState struct {
	fileSet *token.FileSet // fileSet 用于把 AST 位置转换为文件行号。
	file    *ast.File      // file 是当前正在检查的语法树。
	path    string         // path 是当前源码文件路径。
	missing int            // missing 是累计发现的缺失注释数量。
}

// main 检查参数指定目录；未传参数时检查当前目录。
func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}
	state := &auditState{fileSet: token.NewFileSet()}
	if err := filepath.WalkDir(root, state.walk); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if state.missing > 0 {
		fmt.Fprintf(os.Stderr, "Go 注释审计失败：共 %d 处缺失\n", state.missing)
		os.Exit(1)
	}
	fmt.Println("Go 注释审计通过")
}

// walk 过滤非源码和生成目录，并检查单个 Go 文件。
func (s *auditState) walk(path string, entry os.DirEntry, walkErr error) error {
	if walkErr != nil {
		return walkErr
	}
	if entry.IsDir() {
		if entry.Name() == ".git" || entry.Name() == "node_modules" || entry.Name() == "dist" {
			return filepath.SkipDir
		}
		return nil
	}
	if !strings.HasSuffix(path, ".go") {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !strings.Contains(string(content), "@author Andersen") {
		s.missing++
		fmt.Printf("%s:1: 文件缺少 @author Andersen 标识\n", path)
	}
	file, err := parser.ParseFile(s.fileSet, path, nil, parser.ParseComments)
	if err != nil {
		return err
	}
	s.file, s.path = file, path
	for _, declaration := range file.Decls {
		s.inspectTopLevelDeclaration(declaration)
	}
	ast.Walk(s, file)
	return nil
}

// inspectTopLevelDeclaration 检查包级函数、类型、变量和常量的文档注释。
func (s *auditState) inspectTopLevelDeclaration(declaration ast.Decl) {
	switch value := declaration.(type) {
	case *ast.FuncDecl:
		if value.Doc == nil {
			s.report(value.Pos(), "函数", value.Name.Name)
		}
	case *ast.GenDecl:
		for _, specification := range value.Specs {
			switch spec := specification.(type) {
			case *ast.TypeSpec:
				if spec.Doc == nil && value.Doc == nil {
					s.report(spec.Pos(), "类型", spec.Name.Name)
				}
			case *ast.ValueSpec:
				if spec.Doc == nil && value.Doc == nil {
					for _, name := range spec.Names {
						s.report(name.Pos(), value.Tok.String(), name.Name)
					}
				}
			}
		}
	}
}

// Visit 检查任意层级的结构字段、本地常量和匿名函数。
func (s *auditState) Visit(node ast.Node) ast.Visitor {
	switch value := node.(type) {
	case *ast.StructType:
		s.inspectFields(value.Fields)
	case *ast.InterfaceType:
		s.inspectFields(value.Methods)
	case *ast.GenDecl:
		if value.Tok == token.CONST {
			for _, specification := range value.Specs {
				spec := specification.(*ast.ValueSpec)
				if spec.Doc == nil && value.Doc == nil {
					for _, name := range spec.Names {
						if !s.reportedAsTopLevel(value) {
							s.report(name.Pos(), "常量", name.Name)
						}
					}
				}
			}
		}
	case *ast.FuncLit:
		if !s.hasAdjacentComment(value.Pos()) {
			s.report(value.Pos(), "回调", "anonymous")
		}
	}
	return s
}

// inspectFields 检查结构体字段和接口方法是否带有说明。
func (s *auditState) inspectFields(fields *ast.FieldList) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		if field.Doc != nil || field.Comment != nil {
			continue
		}
		name := "embedded"
		if len(field.Names) > 0 {
			names := make([]string, 0, len(field.Names))
			for _, fieldName := range field.Names {
				names = append(names, fieldName.Name)
			}
			name = strings.Join(names, ",")
		}
		s.report(field.Pos(), "字段", name)
	}
}

// hasAdjacentComment 判断匿名函数同一行或前一行是否有专门说明。
func (s *auditState) hasAdjacentComment(position token.Pos) bool {
	line := s.fileSet.Position(position).Line
	for _, group := range s.file.Comments {
		endLine := s.fileSet.Position(group.End()).Line
		if endLine == line || endLine == line-1 {
			return true
		}
	}
	return false
}

// reportedAsTopLevel 判断声明是否已经由包级声明检查处理。
func (s *auditState) reportedAsTopLevel(declaration *ast.GenDecl) bool {
	for _, candidate := range s.file.Decls {
		if candidate == declaration {
			return true
		}
	}
	return false
}

// report 输出一条可定位的注释缺失信息并累加失败计数。
func (s *auditState) report(position token.Pos, kind, name string) {
	s.missing++
	location := s.fileSet.Position(position)
	fmt.Printf("%s:%d: %s %s 缺少注释\n", s.path, location.Line, kind, name)
}
