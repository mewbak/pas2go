// (Try to!) convert a Turbo Pascal AST into Go code

/*
ISSUES:
- string issues: String, TString50, etc
- pointer issues
- handle FILE and FILE OF
- scalar type casting issues: eg i in: EDITOR.PAS:130: VideoWriteText(61+i, 22, i, #219)
- handling of New(), eg: EDITOR.PAS:270 New(state.Lines[i]) -> state.Lines[i+1] = new(TTextWindowLine)
- handling of other builtins, like Val, Move, GetMem, etc
- distinguishing string constants vs char, eg: pArg[1] == "/"
- OopParseDirection and OopCheckCondition calls themselves - causes naming issue with named return value

NICE TO HAVES:
- uses operator precedence rather than ParenExpr
- output Go x+=y for Pascal x=x+y?
*/

package main

import (
	"fmt"
	"io"
	"strings"
)

func Convert(file File, units []*Unit, w io.Writer) {
	c := &converter{w: w}

	c.units = make(map[string]*Unit)
	for _, unit := range units {
		c.units[strings.ToLower(unit.Name)] = unit
	}
	c.types = make(map[string]TypeSpec)
	c.pushScope(ScopeGlobal, nil)

	// Port is predefined by Turbo Pascal, fake it
	min := &ConstExpr{0, false}
	max := &ConstExpr{1000, false}
	c.defineVar("Port", &ArraySpec{min, max, &IdentSpec{&TypeIdent{"", INTEGER}}})

	// TODO: hack - TVideoLine is defined in VIDEO.PAS - do this in separate file
	c.defineType("TVideoLine", &StringSpec{80})

	// TODO: turn panics into ConvertError and catch

	switch file := file.(type) {
	case *Program:
		c.program(file)
	case *Unit:
		c.unit(file)
	default:
		panic(fmt.Sprintf("unhandled File type: %T", file))
	}
}

type converter struct {
	units  map[string]*Unit
	w      io.Writer
	types  map[string]TypeSpec
	scopes []Scope
}

type Scope struct {
	Type      ScopeType
	WithExpr  *VarExpr
	Vars      map[string]TypeSpec
	VarParams map[string]struct{}
}

type ScopeType int

const (
	ScopeNone ScopeType = iota
	ScopeGlobal
	ScopeLocal
	ScopeWith
)

func (c *converter) pushScope(typ ScopeType, withExpr *VarExpr) {
	scope := Scope{typ, withExpr, make(map[string]TypeSpec), make(map[string]struct{})}
	c.scopes = append(c.scopes, scope)
}

func (c *converter) popScope() {
	c.scopes = c.scopes[:len(c.scopes)-1]
}

func (c *converter) defineVar(name string, spec TypeSpec) {
	scope := c.scopes[len(c.scopes)-1]
	scope.Vars[strings.ToLower(name)] = spec
}

func (c *converter) defineWithVar(name string) {
	for i := len(c.scopes) - 1; i >= 0; i-- {
		scope := c.scopes[i]
		if scope.Type != ScopeWith {
			scope.Vars[strings.ToLower(name)] = &IdentSpec{&TypeIdent{name, ILLEGAL}}
			return
		}
	}
}

func (c *converter) defineType(name string, spec TypeSpec) {
	c.types[strings.ToLower(name)] = spec
}

func (c *converter) lookupType(name string) TypeSpec {
	return c.types[strings.ToLower(name)]
}

func (c *converter) lookupVarType(name string) (Scope, TypeSpec) {
	name = strings.ToLower(name)
	for i := len(c.scopes) - 1; i >= 0; i-- {
		scope := c.scopes[i]
		spec := scope.Vars[name]
		if spec != nil {
			return scope, spec
		}
	}
	return Scope{}, nil
}

func (c *converter) setVarParam(name string) {
	scope := c.scopes[len(c.scopes)-1]
	scope.VarParams[strings.ToLower(name)] = struct{}{}
}

func (c *converter) isVarParam(name string) bool {
	name = strings.ToLower(name)
	for i := len(c.scopes) - 1; i >= 0; i-- {
		scope := c.scopes[i]
		_, isVar := scope.VarParams[name]
		if isVar {
			return true
		}
	}
	return false
}

func (c *converter) lookupVarExprType(varExpr *VarExpr) (TypeSpec, string) {
	_, spec := c.lookupVarType(varExpr.Name)
	if spec == nil {
		return nil, ""
	}
	spec = c.lookupIdentSpec(spec)
	if spec == nil {
		return nil, ""
	}

	fieldName := varExpr.Name
	for _, suffix := range varExpr.Suffixes {
		switch suffix := suffix.(type) {
		case *DotSuffix:
			fieldName = suffix.Field
			record := spec.(*RecordSpec)
			spec = findField(record, suffix.Field)
			if spec == nil {
				panic(fmt.Sprintf("field not found: %q", suffix.Field))
			}
		case *IndexSuffix:
			switch specTyped := spec.(type) {
			case *ArraySpec:
				spec = specTyped.Of
			case *StringSpec, *IdentSpec:
			default:
				panic(fmt.Sprintf("unexpected index type: %s", spec))
			}
		case *PointerSuffix:
			pointer := spec.(*PointerSpec)
			spec = &IdentSpec{pointer.Type}
		}
		spec = c.lookupIdentSpec(spec)
		if spec == nil {
			return nil, ""
		}
	}

	return spec, fieldName
}

func (c *converter) lookupIdentSpec(spec TypeSpec) TypeSpec {
	ident, isIdent := spec.(*IdentSpec)
	if !isIdent {
		return spec
	}
	if ident.Name == "" {
		return spec // builtin type
	}
	spec = c.lookupType(ident.Name)
	if spec == nil {
		return nil
	}
	return spec
}

func (c *converter) lookupNamedType(spec TypeSpec) TypeSpec {
	if a, ok := spec.(*ArraySpec); ok {
		spec = a.Of
	}
	typeName := spec.(*IdentSpec).Name
	spec = c.lookupType(typeName)
	if spec == nil {
		panic(fmt.Sprintf("named type not found: %q", typeName))
	}
	return spec
}

func findField(record *RecordSpec, field string) TypeSpec {
	for _, section := range record.Sections {
		for _, name := range section.Names {
			if name == field {
				return section.Type
			}
		}
	}
	return nil
}

func (c *converter) print(a ...interface{}) {
	fmt.Fprint(c.w, a...)
}

func (c *converter) printf(format string, a ...interface{}) {
	fmt.Fprintf(c.w, format, a...)
}

func (c *converter) program(program *Program) {
	c.print("package main\n\n")
	if program.Uses != nil {
		c.printf("// uses: %s\n\n", strings.Join(program.Uses, ", "))
		for _, unitName := range program.Uses {
			c.addUnitDecls(unitName)
		}
	}
	c.decls(program.Decls, true)
	c.defineDecls(program.Decls)
	c.print("func main() {\n")
	c.stmts(program.Stmt.Stmts)
	c.print("}\n")
}

func (c *converter) addUnitDecls(unitName string) {
	unit, loaded := c.units[strings.ToLower(unitName)]
	if !loaded {
		return
	}
	c.defineDecls(unit.Interface)
}

func (c *converter) defineDecls(decls []DeclPart) {
	for _, decl := range decls {
		switch decl := decl.(type) {
		case *TypeDefs:
			for _, d := range decl.Defs {
				c.defineType(d.Name, d.Type)
			}
		case *VarDecls:
			for _, d := range decl.Decls {
				for _, name := range d.Names {
					c.defineVar(name, d.Type)
				}
			}
		case *ConstDecls:
			for _, d := range decl.Decls {
				c.defineVar(d.Name, d.Type)
			}
		case *ProcDecl:
			c.defineVar(decl.Name, &ProcSpec{decl.Params})
		case *FuncDecl:
			c.defineVar(decl.Name, &FuncSpec{decl.Params, decl.Result})
		}
	}
}

func (c *converter) defineParams(params []*ParamGroup) {
	for _, group := range params {
		for _, name := range group.Names {
			c.defineVar(name, &IdentSpec{group.Type})
			if group.IsVar {
				c.setVarParam(name)
			}
		}
	}
}

func (c *converter) unit(unit *Unit) {
	c.printf("package main // unit: %s\n\n", unit.Name)
	if unit.InterfaceUses != nil {
		c.printf("// interface uses: %s\n\n", strings.Join(unit.InterfaceUses, ", "))
		for _, unitName := range unit.InterfaceUses {
			c.addUnitDecls(unitName)
		}
	}
	c.decls(unit.Interface, true)
	c.defineDecls(unit.Interface)
	if unit.ImplementationUses != nil {
		c.printf("\n// implementation uses: %s\n\n", strings.Join(unit.ImplementationUses, ", "))
		for _, unitName := range unit.ImplementationUses {
			c.addUnitDecls(unitName)
		}
	}
	c.defineDecls(unit.Implementation)
	c.decls(unit.Implementation, true)
	c.print("func init() {\n")
	c.stmts(unit.Init.Stmts)
	c.print("}\n")
}

func (c *converter) decls(decls []DeclPart, isMain bool) {
	for _, decl := range decls {
		c.decl(decl, isMain)
	}
}

func (c *converter) decl(decl DeclPart, isMain bool) {
	switch decl := decl.(type) {
	case *ConstDecls:
		consts := []*ConstDecl{}
		vars := []*ConstDecl{}
		for _, d := range decl.Decls {
			switch d.Value.(type) {
			case *ConstArrayExpr, *ConstRecordExpr:
				vars = append(vars, d)
			default:
				consts = append(consts, d)
			}
		}
		if len(consts) > 0 {
			if len(consts) == 1 {
				c.print("const ")
			} else {
				c.print("const (\n")
			}
			for _, d := range consts {
				c.printf("%s", d.Name)
				if d.Type != nil {
					c.print(" ")
					c.typeSpec(d.Type)
				}
				c.print(" = ")
				c.expr(d.Value)
				c.print("\n")
			}
			if len(consts) != 1 {
				c.print(")\n")
			}
		}
		if len(vars) > 0 {
			if len(vars) == 1 {
				c.print("var ")
			} else {
				c.print("var (\n")
			}
			for _, d := range vars {
				c.printf("%s ", d.Name)
				c.typeSpec(d.Type)
				c.print(" = ")
				if _, isConstRecord := d.Value.(*ConstRecordExpr); isConstRecord {
					c.typeSpec(d.Type)
				}
				c.expr(d.Value)
				c.print("\n")
			}
			if len(vars) != 1 {
				c.print(")\n")
			}
		}
	case *FuncDecl:
		if decl.Stmt == nil {
			return
		}
		if isMain {
			c.printf("func %s(", decl.Name)
		} else {
			c.printf("%s := func(", decl.Name)
		}
		c.params(decl.Params)
		c.printf(") (%s ", decl.Name)
		c.typeIdent(decl.Result)
		c.print(") {\n")

		c.pushScope(ScopeLocal, nil)
		c.defineParams(decl.Params)
		c.defineDecls(decl.Decls)
		c.decls(decl.Decls, false)
		c.stmts(decl.Stmt.Stmts)
		c.popScope()

		c.print("return\n}\n\n")
	case *LabelDecls:
		// not needed
	case *ProcDecl:
		if decl.Stmt == nil {
			return
		}
		if isMain {
			c.printf("func %s(", decl.Name)
		} else {
			c.printf("%s := func(", decl.Name)
		}
		c.params(decl.Params)
		c.print(") {\n")

		c.pushScope(ScopeLocal, nil)
		c.defineParams(decl.Params)
		c.defineDecls(decl.Decls)
		c.decls(decl.Decls, false)
		c.stmts(decl.Stmt.Stmts)
		c.popScope()

		c.print("}\n\n")
	case *TypeDefs:
		if len(decl.Defs) == 1 {
			c.print("type ")
		} else {
			c.print("type (\n")
		}
		var scalarType string
		var scalarConsts []string
		for _, d := range decl.Defs {
			c.printf("%s ", d.Name)
			if spec, ok := d.Type.(*ScalarSpec); ok {
				scalarType = d.Name
				scalarConsts = spec.Names
			}
			c.typeSpec(d.Type)
			c.print("\n")
		}
		if len(decl.Defs) != 1 {
			c.print(")\n")
		}
		if scalarConsts != nil {
			// Add constants from last ScalarSpec "enum". Bit of a
			// hack, as it only supports one "enum" per defs, but
			// that's all we need for ZZT source.
			c.print("const (\n")
			for i, name := range scalarConsts {
				c.printf("%s", name)
				if i == 0 {
					c.printf(" %s = iota + 1", scalarType)
				}
				c.print("\n")
			}
			c.print(")\n\n")
		}
	case *VarDecls:
		if len(decl.Decls) == 1 {
			c.print("var ")
		} else {
			c.print("var (\n")
		}
		for _, d := range decl.Decls {
			c.printf("%s ", strings.Join(d.Names, ", "))
			c.typeSpec(d.Type)
			c.print("\n")
		}
		if len(decl.Decls) != 1 {
			c.print(")\n")
		}
	default:
		panic(fmt.Sprintf("unhandled DeclPart type: %T", decl))
	}
}

func (c *converter) params(params []*ParamGroup) {
	for i, param := range params {
		if i > 0 {
			c.print(", ")
		}
		c.print(strings.Join(param.Names, ", "), " ")
		if param.IsVar {
			c.print("*")
		}
		c.typeIdent(param.Type)
	}
}

func (c *converter) typeIdent(typ *TypeIdent) {
	var s string
	switch typ.Builtin {
	case CHAR:
		s = "byte"
	case BOOLEAN:
		s = "bool"
	case INTEGER:
		s = "int16"
	case REAL:
		s = "float64"
	case STRING:
		s = "string"
	default:
		switch strings.ToLower(typ.Name) {
		case "pointer":
			s = "uintptr" // TODO: change to *byte?
		case "word":
			s = "uint16"
		case "longint":
			s = "int32"
		default:
			s = typ.Name
		}
	}
	c.print(s)
}

func (c *converter) stmts(stmts []Stmt) {
	for _, stmt := range stmts {
		c.stmt(stmt)
	}
}

func (c *converter) stmtNoBraces(stmt Stmt) {
	switch stmt := stmt.(type) {
	case *CompoundStmt:
		c.stmts(stmt.Stmts)
	default:
		c.stmt(stmt)
	}
}

func (c *converter) stmt(stmt Stmt) {
	switch stmt := stmt.(type) {
	case *AssignStmt:
		// TODO: handle TypeConv?
		c.varExpr(stmt.Var, false)
		c.print(" = ")
		c.expr(stmt.Value)
	case *CaseStmt:
		c.print("switch ")
		c.expr(stmt.Selector)
		c.print(" {\n")
		for _, cas := range stmt.Cases {
			c.print("case ")
			if rangeExpr, ok := cas.Consts[0].(*RangeExpr); ok {
				// Making a lot of assumptions here, but this is the only
				// way it's used in the ZZT source.
				min := rangeExpr.Min.(*ConstExpr).Value.(string)[0]
				max := rangeExpr.Max.(*ConstExpr).Value.(string)[0]
				for i, b := 0, min; b <= max; i, b = i+1, b+1 {
					if i > 0 {
						c.print(", ")
					}
					c.printf("'%c'", b)
				}
			} else {
				c.exprs(cas.Consts)
			}
			c.print(":\n")
			c.stmtNoBraces(cas.Stmt)
		}
		if stmt.Else != nil {
			c.print("default:\n")
			c.stmts(stmt.Else)
		}
		c.print("}")
	case *CompoundStmt:
		c.print("{\n")
		c.stmts(stmt.Stmts)
		c.print("}")
	case *EmptyStmt:
		return
	case *ForStmt:
		c.printf("for %s = ", stmt.Var)
		c.expr(stmt.Initial)
		if stmt.Down {
			c.printf("; %s >= ", stmt.Var)
			c.expr(stmt.Final)
			c.printf("; %s-- {\n", stmt.Var)
		} else {
			c.printf("; %s <= ", stmt.Var)
			c.expr(stmt.Final)
			c.printf("; %s++ {\n", stmt.Var)
		}
		c.stmtNoBraces(stmt.Stmt)
		c.print("}")
	case *GotoStmt:
		c.printf("goto %s", stmt.Label)
	case *IfStmt:
		c.print("if ")
		c.expr(stmt.Cond)
		c.print(" {\n")
		c.stmtNoBraces(stmt.Then)
		c.print("}")
		if stmt.Else != nil {
			innerIf, isElseIf := stmt.Else.(*IfStmt)
			if isElseIf {
				c.print(" else ")
				c.stmtNoBraces(innerIf)
			} else {
				c.print(" else {\n")
				c.stmtNoBraces(stmt.Else)
				c.print("}")
			}
		}
	case *LabelledStmt:
		c.printf("%s:\n", stmt.Label)
		c.stmt(stmt.Stmt)
	case *ProcStmt:
		procStr := strings.ToLower(stmt.Proc.String())
		switch procStr {
		case "exit":
			c.print("return")
		case "str":
			if widthExpr, isWidth := stmt.Args[0].(*WidthExpr); isWidth {
				c.print("StrWidth(")
				c.procArg(false, stmt.Args[0])
				c.printf(", %d", widthExpr.Width.(*ConstExpr).Value.(int))
				c.print(", ")
			} else {
				c.print("Str(")
				c.procArg(false, stmt.Args[0])
				c.print(", ")
			}
			c.expr(stmt.Args[1])
			c.print(")")
		default:
			if procStr == "delete" {
				c.varExpr(stmt.Args[0].(*VarExpr), false)
				c.print(" = ")
			}
			c.varExpr(stmt.Proc, false)
			spec, _ := c.lookupVarExprType(stmt.Proc)
			var params []*ParamGroup
			if spec != nil {
				params = spec.(*ProcSpec).Params
			}
			c.print("(")
			c.procArgs(params, stmt.Args)
			c.print(")")
		}
	case *RepeatStmt:
		c.print("for {\n")
		c.stmts(stmt.Stmts)
		c.print("if ")
		c.expr(stmt.Cond)
		c.print(" {\nbreak\n}\n}")
	case *WhileStmt:
		c.print("for ")
		c.expr(stmt.Cond)
		c.print(" {\n")
		c.stmtNoBraces(stmt.Stmt)
		c.print("}")
	case *WithStmt:
		spec, fieldName := c.lookupVarExprType(stmt.Var)
		if spec == nil {
			panic(fmt.Sprintf("'with' statement var not found: %s", stmt.Var))
		}
		record := spec.(*RecordSpec)
		var withName string
		if len(stmt.Var.Suffixes) == 0 && strings.ToLower(fieldName) == strings.ToLower(stmt.Var.Name) {
			withName = stmt.Var.Name
		} else {
			withName = c.makeWithName(fieldName)
			c.printf("%s := &", withName)
			c.varExpr(stmt.Var, false)
			c.print("\n")
			c.defineWithVar(withName)
		}
		c.pushScope(ScopeWith, &VarExpr{Name: withName})
		for _, section := range record.Sections {
			for _, name := range section.Names {
				c.defineVar(name, section.Type)
			}
		}
		c.stmtNoBraces(stmt.Stmt)
		c.popScope()
	default:
		panic(fmt.Sprintf("unhandled Stmt: %T", stmt))
	}
	c.print("\n")
}

func (c *converter) procArgs(params []*ParamGroup, args []Expr) {
	isVars := []bool{}
	for _, group := range params {
		for range group.Names {
			isVars = append(isVars, group.IsVar)
		}
	}
	for i, arg := range args {
		if i > 0 {
			c.print(", ")
		}
		if params != nil {
			// TODO: this means builtin functions will have targetIsVar=false,
			// but that's not true of some, eg: Dec() -- need to define these manually?
			c.procArg(isVars[i], arg)
		} else {
			c.procArg(false, arg)
		}
	}
}

func (c *converter) procArg(targetIsVar bool, arg Expr) {
	switch arg := arg.(type) {
	case *VarExpr:
		if len(arg.Suffixes) == 0 {
			if arg.HasAt {
				panic(fmt.Sprintf("unexpected HasAt: %q", arg.Name))
			}
			isVar := c.isVarParam(arg.Name)
			switch {
			case isVar && targetIsVar:
				c.varExpr(arg, true) // pass pointer straight through
			case isVar && !targetIsVar:
				c.print("*")
				c.varExpr(arg, true)
			case !isVar && targetIsVar:
				c.print("&")
				c.varExpr(arg, true)
			default: // !isVar && !targetIsVar
				c.varExpr(arg, true)
			}
		} else {
			// TODO: do we ever need * or & in these cases?
			c.expr(arg)
		}
	default:
		c.expr(arg)
	}
}

func (c *converter) makeWithName(name string) string {
	parts := splitCamel(name)
	lastPart := parts[len(parts)-1]
	withName := strings.ToLower(strings.TrimSuffix(lastPart, "s"))
	if _, spec := c.lookupVarType(withName); spec == nil {
		return withName
	}
	for i := 2; i < 10; i++ {
		numName := withName + fmt.Sprint(i)
		if _, spec := c.lookupVarType(numName); spec == nil {
			return numName
		}
	}
	panic(fmt.Sprintf("too many tries generating 'with' name: %s", withName))
}

func splitCamel(name string) []string {
	parts := []string{}
	hadCap := false
	start := 0
	for i, c := range name {
		if hadCap && c >= 'a' && c <= 'z' && i > 1 {
			parts = append(parts, name[start:i-1])
			start = i - 1
		}
		hadCap = c >= 'A' && c <= 'Z'
	}
	parts = append(parts, name[start:])
	return parts
}

func (c *converter) exprs(exprs []Expr) {
	for i, expr := range exprs {
		if i > 0 {
			c.print(", ")
		}
		c.expr(expr)
	}
}

func (c *converter) expr(expr Expr) {
	switch expr := expr.(type) {
	case *BinaryExpr:
		if expr.Op == IN {
			c.inExpr(expr)
			return
		}
		c.expr(expr.Left)
		var opStr string
		if expr.Op == AND || expr.Op == OR || expr.Op == XOR {
			// This is cheating; should really use types, but this works with most code
			_, isConst := expr.Right.(*ConstExpr)
			if isConst {
				opStr = bitwiseOperatorStr(expr.Op)
			} else {
				opStr = operatorStr(expr.Op)
			}
		} else {
			opStr = operatorStr(expr.Op)
		}
		c.printf(" %s ", opStr)
		c.expr(expr.Right)
	case *ConstExpr:
		switch value := expr.Value.(type) {
		case string:
			if len(value) == 1 {
				c.printf("%q", value[0])
			} else {
				c.printf("%q", value)
			}
		case float64:
			s := fmt.Sprintf("%g", value)
			if !strings.Contains(s, ".") {
				s += ".0"
			}
			c.print(s)
		case nil:
			c.print("nil")
		default:
			if expr.IsHex {
				c.printf("0x%02X", value)
			} else {
				c.printf("%v", value)
			}
		}
	case *ConstArrayExpr:
		c.print("[...]string{") // TODO: not necessarily string
		c.exprs(expr.Values)
		c.print("}")
	case *ConstRecordExpr:
		c.print("{")
		for i, field := range expr.Fields {
			if i > 0 {
				c.print(", ")
			}
			c.print(field.Name)
			c.print(": ")
			c.expr(field.Value)
		}
		c.print("}")
	case *FuncExpr:
		c.varExpr(expr.Func, false)
		spec, _ := c.lookupVarExprType(expr.Func)
		var params []*ParamGroup
		if spec != nil {
			params = spec.(*FuncSpec).Params
		}
		c.print("(")
		c.procArgs(params, expr.Args)
		c.print(")")
	case *ParenExpr:
		c.print("(")
		c.expr(expr.Expr)
		c.print(")")
	case *PointerExpr:
		c.expr(expr.Expr)
	case *RangeExpr:
		panic("unexpected RangeExpr: should be handled by 'case' and 'in'")
	case *SetExpr:
		panic("unexpected SetExpr: should be handled by 'in'")
	case *TypeConvExpr:
		c.typeIdent(&TypeIdent{"", expr.Type})
		c.print("(")
		c.expr(expr.Expr)
		c.print(")")
	case *UnaryExpr:
		c.print(operatorStr(expr.Op))
		c.expr(expr.Expr)
	case *VarExpr:
		c.varExpr(expr, false)
		spec, _ := c.lookupVarExprType(expr)
		if spec != nil {
			_, isFunc := spec.(*FuncSpec)
			if isFunc {
				// Pascal allows function call without parens
				c.print("()")
			}
		}
	case *WidthExpr:
		// Width itself is handled in ProcStmt "str" case
		c.expr(expr.Expr)
	default:
		c.printf("%s", expr)
	}
}

func (c *converter) varExpr(expr *VarExpr, suppressStar bool) {
	isVar := len(expr.Suffixes) == 0 && c.isVarParam(expr.Name)
	if expr.HasAt && isVar {
		panic(fmt.Sprintf("unexpected @ with var param: %s", expr))
	}
	if isVar && !suppressStar {
		c.printf("*")
	} else if expr.HasAt {
		c.printf("&")
	}
	if len(expr.Suffixes) == 0 {
		// If record field name is being used inside "with"
		// statement, prefix it with the with expression and ".".
		scope, spec := c.lookupVarType(expr.Name)
		if spec != nil && scope.Type == ScopeWith {
			c.varExpr(scope.WithExpr, true)
			c.print(".")
		}
	}
	c.print(expr.Name)
	for i, suffix := range expr.Suffixes {
		switch suffix := suffix.(type) {
		case *IndexSuffix:
			// Look up var + suffixes so far and add Min array index if not 0
			varExprSoFar := &VarExpr{false, expr.Name, expr.Suffixes[:i]}
			spec, _ := c.lookupVarExprType(varExprSoFar)
			if spec == nil {
				panic(fmt.Sprintf("array not found: %s", varExprSoFar))
			}

			var min int
			switch spec := spec.(type) {
			case *ArraySpec:
				min = spec.Min.(*ConstExpr).Value.(int)
			case *StringSpec:
				min = 1
			}

			c.print("[")
			if min != 0 {
				switch index := suffix.Index.(type) {
				case *ConstExpr:
					val := index.Value.(int)
					c.printf("%d", val-min)
				case *FuncExpr, *ParenExpr, *PointerExpr, *TypeConvExpr, *UnaryExpr, *VarExpr:
					c.expr(suffix.Index)
					c.printf(" - %d", min)
				default:
					c.print("(")
					c.expr(suffix.Index)
					c.printf(") - %d", min)
				}
			} else {
				c.expr(suffix.Index)
			}
			c.print("]")
		case *DotSuffix:
			c.print(".", suffix.Field)
		case *PointerSuffix:
		default:
			panic(fmt.Sprintf("unhandled VarSuffix: %T", suffix))
		}
	}
}

func (c *converter) inExpr(expr *BinaryExpr) {
	c.print("(")
	values := expr.Right.(*SetExpr)
	for i, value := range values.Values {
		if i > 0 {
			c.print(" || ")
		}
		if rangeExpr, ok := value.(*RangeExpr); ok {
			c.expr(expr.Left)
			c.print(">=")
			c.expr(rangeExpr.Min)
			c.print(" && ")
			c.expr(expr.Left)
			c.print("<=")
			c.expr(rangeExpr.Max)
		} else {
			c.expr(expr.Left)
			c.print("==")
			c.expr(value)
		}
	}
	c.print(")")
}

func (c *converter) typeSpec(spec TypeSpec) {
	switch spec := spec.(type) {
	case *FuncSpec:
		c.print("func(")
		c.params(spec.Params)
		c.print(") ")
		c.typeIdent(spec.Result)
	case *ProcSpec:
		c.print("func(")
		c.params(spec.Params)
		c.print(")")
	case *ScalarSpec:
		// spec.Names are defined by TypeDefs handling
		c.print("uint8")
	case *IdentSpec:
		c.typeIdent(spec.TypeIdent)
	case *StringSpec:
		// TODO: how to handle string sizes? should we use [Size]byte
		c.print("string")
	case *ArraySpec:
		min := spec.Min.(*ConstExpr).Value.(int)
		maxConstExpr, maxIsConst := spec.Max.(*ConstExpr)
		if maxIsConst {
			c.printf("[%d]", maxConstExpr.Value.(int)-min+1)
		} else {
			c.print("[")
			c.expr(spec.Max)
			switch {
			case min < 1:
				c.printf("+%d", 1-min)
			case min > 1:
				c.printf("-%d", min-1)
			}
			c.print("]")
		}
		c.typeSpec(spec.Of)
	case *RecordSpec:
		c.print("struct {\n")
		for _, section := range spec.Sections {
			c.print(strings.Join(section.Names, ", "), " ")
			c.typeSpec(section.Type)
			c.print("\n")
		}
		c.print("}")
	case *FileSpec:
		// TODO: handle Of, how to handle FILE?
		c.print("FILE")
	case *PointerSpec:
		c.print("*")
		c.typeIdent(spec.Type)
	default:
		c.printf("%s", spec)
	}
}

func operatorStr(op Token) string {
	switch op {
	case EQUALS:
		return "=="
	case NOT_EQUALS:
		return "!="
	case OR:
		return "||"
	case XOR:
		return "!="
	case DIV:
		return "/"
	case MOD:
		return "%"
	case AND:
		return "&&"
	case SHL:
		return "<<"
	case SHR:
		return ">>"
	case NOT:
		return "!"
	default:
		// same as in Pascal
		return op.String()
	}
}

func bitwiseOperatorStr(op Token) string {
	switch op {
	case AND:
		return "&"
	case OR:
		return "|"
	case XOR:
		return "^"
	default:
		panic(fmt.Sprintf("unexpected operator: %s", op))
	}
}
