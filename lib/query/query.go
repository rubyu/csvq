package query

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mithrandie/csvq/lib/cmd"
	"github.com/mithrandie/csvq/lib/parser"
	"github.com/mithrandie/csvq/lib/ternary"
)

type StatementType int

const (
	SELECT StatementType = iota
	INSERT
	UPDATE
	DELETE
	CREATE_TABLE
	ADD_COLUMNS
	DROP_COLUMNS
	RENAME_COLUMN
	PRINT
)

type StatementFlow int

const (
	TERMINATE StatementFlow = iota
	ERROR
	EXIT
	BREAK
	CONTINUE
)

var GlobalVars = Variables{}
var ViewCache = NewViewMap()
var Cursors = CursorMap{}

type Result struct {
	Type          StatementType
	View          *View
	FileInfo      *FileInfo
	OperatedCount int
	Log           string
}

var ResultSet = []Result{}

func Execute(input string) (string, error) {
	var out string

	program, err := parser.Parse(input)
	if err != nil {
		return out, err
	}

	flow, log, err := ExecuteProgram(program)
	out += log

	if flow == TERMINATE {
		log, err = Commit()
		out += log
	}

	return out, err
}

func ExecuteProgram(program []parser.Statement) (StatementFlow, string, error) {
	flow := TERMINATE

	var out string
	for _, stmt := range program {
		f, log, err := ExecuteStatement(stmt)
		out += log
		if err != nil {
			return f, out, err
		}
		if f != TERMINATE {
			flow = f
			break
		}
	}
	return flow, out, nil
}

func ExecuteStatement(stmt parser.Statement) (StatementFlow, string, error) {
	flow := TERMINATE

	GlobalVars.ClearAutoIncrement()
	ViewCache.ClearAliases()

	var log string
	var err error

	var results []Result
	var view *View
	var views []*View
	var printstr string

	switch stmt.(type) {
	case parser.SetFlag:
		err = SetFlag(stmt.(parser.SetFlag))
	case parser.VariableDeclaration:
		err = GlobalVars.Decrare(stmt.(parser.VariableDeclaration), Filter{})
	case parser.VariableSubstitution:
		_, err = GlobalVars.Substitute(stmt.(parser.VariableSubstitution), Filter{})
	case parser.CursorDeclaration:
		decl := stmt.(parser.CursorDeclaration)
		err = Cursors.Add(decl.Cursor.Literal, decl.Query)
	case parser.OpenCursor:
		err = Cursors.Open(stmt.(parser.OpenCursor).Cursor.Literal)
	case parser.CloseCursor:
		err = Cursors.Close(stmt.(parser.CloseCursor).Cursor.Literal)
	case parser.DisposeCursor:
		Cursors.Dispose(stmt.(parser.DisposeCursor).Cursor.Literal)
	case parser.FetchCursor:
		fetch := stmt.(parser.FetchCursor)
		_, err = FetchCursor(fetch.Cursor.Literal, fetch.Position, fetch.Variables)
	case parser.TableDeclaration:
		err = DeclareTable(stmt.(parser.TableDeclaration))
	case parser.SelectQuery:
		if view, err = Select(stmt.(parser.SelectQuery)); err == nil {
			results = []Result{
				{
					Type: SELECT,
					View: view,
				},
			}
		}
	case parser.InsertQuery:
		if view, err = Insert(stmt.(parser.InsertQuery)); err == nil {
			results = []Result{
				{
					Type:          INSERT,
					FileInfo:      view.FileInfo,
					OperatedCount: view.OperatedRecords,
					Log:           fmt.Sprintf("%s inserted on %q", formatCount(view.OperatedRecords, "record"), view.FileInfo.Path),
				},
			}
			view.OperatedRecords = 0
		}
	case parser.UpdateQuery:
		if views, err = Update(stmt.(parser.UpdateQuery)); err == nil {
			results = make([]Result, len(views))
			for i, v := range views {
				results[i] = Result{
					Type:          UPDATE,
					FileInfo:      v.FileInfo,
					OperatedCount: v.OperatedRecords,
					Log:           fmt.Sprintf("%s updated on %q", formatCount(v.OperatedRecords, "record"), v.FileInfo.Path),
				}
				v.OperatedRecords = 0
			}
		}
	case parser.DeleteQuery:
		if views, err = Delete(stmt.(parser.DeleteQuery)); err == nil {
			results = make([]Result, len(views))
			for i, v := range views {
				results[i] = Result{
					Type:          DELETE,
					FileInfo:      v.FileInfo,
					OperatedCount: v.OperatedRecords,
					Log:           fmt.Sprintf("%s deleted on %q", formatCount(v.OperatedRecords, "record"), v.FileInfo.Path),
				}
				v.OperatedRecords = 0
			}
		}
	case parser.CreateTable:
		if view, err = CreateTable(stmt.(parser.CreateTable)); err == nil {
			results = []Result{
				{
					Type:     CREATE_TABLE,
					FileInfo: view.FileInfo,
					Log:      fmt.Sprintf("file %q is created", view.FileInfo.Path),
				},
			}
			view.OperatedRecords = 0
		}
	case parser.AddColumns:
		if view, err = AddColumns(stmt.(parser.AddColumns)); err == nil {
			results = []Result{
				{
					Type:          ADD_COLUMNS,
					FileInfo:      view.FileInfo,
					OperatedCount: view.OperatedFields,
					Log:           fmt.Sprintf("%s added on %q", formatCount(view.OperatedFields, "field"), view.FileInfo.Path),
				},
			}
			view.OperatedRecords = 0
		}
	case parser.DropColumns:
		if view, err = DropColumns(stmt.(parser.DropColumns)); err == nil {
			results = []Result{
				{
					Type:          DROP_COLUMNS,
					FileInfo:      view.FileInfo,
					OperatedCount: view.OperatedFields,
					Log:           fmt.Sprintf("%s dropped on %q", formatCount(view.OperatedFields, "field"), view.FileInfo.Path),
				},
			}
			view.OperatedRecords = 0
		}
	case parser.RenameColumn:
		if view, err = RenameColumn(stmt.(parser.RenameColumn)); err == nil {
			results = []Result{
				{
					Type:          RENAME_COLUMN,
					FileInfo:      view.FileInfo,
					OperatedCount: view.OperatedFields,
					Log:           fmt.Sprintf("%s renamed on %q", formatCount(view.OperatedFields, "field"), view.FileInfo.Path),
				},
			}
			view.OperatedRecords = 0
		}
	case parser.TransactionControl:
		switch stmt.(parser.TransactionControl).Token {
		case parser.COMMIT:
			log, err = Commit()
		case parser.ROLLBACK:
			log = Rollback()
		}
	case parser.FlowControl:
		switch stmt.(parser.FlowControl).Token {
		case parser.CONTINUE:
			flow = CONTINUE
		case parser.BREAK:
			flow = BREAK
		case parser.EXIT:
			flow = EXIT
		}
	case parser.If:
		flow, log, err = IfStmt(stmt.(parser.If))
	case parser.While:
		flow, log, err = While(stmt.(parser.While))
	case parser.WhileInCursor:
		flow, log, err = WhileInCursor(stmt.(parser.WhileInCursor))
	case parser.Print:
		if printstr, err = Print(stmt.(parser.Print)); err == nil {
			results = []Result{
				{
					Type: PRINT,
					Log:  printstr,
				},
			}
		}
	}

	if results != nil {
		ResultSet = append(ResultSet, results...)
	}

	if err != nil {
		flow = ERROR
	}
	return flow, log, err
}

func IfStmt(stmt parser.If) (StatementFlow, string, error) {
	stmts := make([]parser.ElseIf, len(stmt.ElseIf)+1)
	stmts[0] = parser.ElseIf{
		Condition:  stmt.Condition,
		Statements: stmt.Statements,
	}
	for i, v := range stmt.ElseIf {
		stmts[i+1] = v.(parser.ElseIf)
	}

	var filter Filter
	for _, v := range stmts {
		p, err := filter.Evaluate(v.Condition)
		if err != nil {
			return ERROR, "", err
		}
		if p.Ternary() == ternary.TRUE {
			return ExecuteProgram(v.Statements)
		}
	}

	if stmt.Else != nil {
		return ExecuteProgram(stmt.Else.(parser.Else).Statements)
	}
	return TERMINATE, "", nil
}

func While(stmt parser.While) (StatementFlow, string, error) {
	var out string

	var filter Filter
	for {
		p, err := filter.Evaluate(stmt.Condition)
		if err != nil {
			return ERROR, out, err
		}
		if p.Ternary() != ternary.TRUE {
			break
		}
		f, s, err := ExecuteProgram(stmt.Statements)
		out += s
		if err != nil {
			return ERROR, out, err
		}

		if f == BREAK {
			return TERMINATE, out, nil
		}
		if f == EXIT {
			return EXIT, out, nil
		}
	}
	return TERMINATE, out, nil
}

func WhileInCursor(stmt parser.WhileInCursor) (StatementFlow, string, error) {
	var out string

	for {
		success, err := FetchCursor(stmt.Cursor.Literal, nil, stmt.Variables)
		if err != nil {
			return ERROR, out, err
		}
		if !success {
			break
		}

		f, s, err := ExecuteProgram(stmt.Statements)
		out += s
		if err != nil {
			return ERROR, out, err
		}

		if f == BREAK {
			return TERMINATE, out, nil
		}
		if f == EXIT {
			return EXIT, out, nil
		}
	}

	return TERMINATE, out, nil
}

func FetchCursor(name string, fetchPosition parser.Expression, vars []parser.Variable) (bool, error) {
	position := parser.NEXT
	number := -1
	if fetchPosition != nil {
		fp := fetchPosition.(parser.FetchPosition)
		position = fp.Position.Token
		if fp.Number != nil {
			var filter Filter
			p, err := filter.Evaluate(fp.Number)
			if err != nil {
				return false, err
			}
			i := parser.PrimaryToInteger(p)
			if parser.IsNull(i) {
				return false, errors.New(fmt.Sprintf("fetch position %s is not a integer", fp.Number))
			}
			number = int(i.(parser.Integer).Value())
		}
	}

	primaries, err := Cursors.Fetch(name, position, number)
	if err != nil {
		return false, err
	}
	if primaries == nil {
		return false, nil
	}
	if len(vars) != len(primaries) {
		return false, errors.New(fmt.Sprintf("cursor %s field length does not match variables number", name))
	}

	var filter Filter
	for i, v := range vars {
		substitution := parser.VariableSubstitution{
			Variable: v,
			Value:    primaries[i],
		}
		_, err := GlobalVars.Substitute(substitution, filter)
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

func DeclareTable(expr parser.TableDeclaration) error {
	if _, ok := ViewCache.Exists(expr.Table.Literal); ok {
		return errors.New(fmt.Sprintf("table %s already exists", expr.Table.Literal))
	}

	var view *View
	var err error

	if expr.Query != nil {
		view, err = Select(expr.Query.(parser.SelectQuery))
		if err != nil {
			return err
		}

		if err := view.UpdateHeader(expr.Table.Literal, expr.Fields); err != nil {
			return err
		}
	} else {
		fields := make([]string, len(expr.Fields))
		for i, v := range expr.Fields {
			f, _ := v.(parser.Identifier)
			if InStrSlice(f.Literal, fields) {
				return errors.New(fmt.Sprintf("field %s is duplicate", f))
			}
			fields[i] = f.Literal
		}
		header := NewHeaderWithoutId(expr.Table.Literal, fields)
		view = &View{
			Header: header,
		}
	}

	view.FileInfo = &FileInfo{
		Path:      expr.Table.Literal,
		Temporary: true,
	}

	ViewCache.Set(view, expr.Table.Literal)

	return err
}

func formatCount(i int, obj string) string {
	var s string
	if i == 0 {
		s = fmt.Sprintf("no %s", obj)
	} else if i == 1 {
		s = fmt.Sprintf("%d %s", i, obj)
	} else {
		s = fmt.Sprintf("%d %ss", i, obj)
	}
	return s
}

func Commit() (string, error) {
	flags := cmd.GetFlags()
	var out string

	var createFiles = map[string]*FileInfo{}
	var updateFiles = map[string]*FileInfo{}

	for _, result := range ResultSet {
		if result.View != nil {
			//SELECT
			viewstr, err := EncodeView(result.View, flags.Format, flags.WriteDelimiter, flags.WithoutHeader, flags.WriteEncoding, flags.LineBreak)
			if err != nil {
				return out, err
			}
			out += viewstr
		} else if result.FileInfo != nil {
			//CREATE or UPDATE
			switch result.Type {
			case CREATE_TABLE:
				createFiles[result.FileInfo.Path] = result.FileInfo
			default:
				if !result.FileInfo.Temporary && 0 < result.OperatedCount {
					if _, ok := createFiles[result.FileInfo.Path]; !ok {
						if _, ok := updateFiles[result.FileInfo.Path]; !ok {
							updateFiles[result.FileInfo.Path] = result.FileInfo
						}
					}
				}
			}
		}

		if 0 < len(result.Log) {
			out += result.Log + "\n"
		}
	}

	var modified bool

	if 0 < len(createFiles) {
		for pt, fi := range createFiles {
			view, _ := ViewCache.Get(pt)
			viewstr, err := EncodeView(view, cmd.CSV, fi.Delimiter, false, fi.Encoding, fi.LineBreak)
			if err != nil {
				return out, err
			}

			if err = cmd.CreateFile(pt, viewstr); err != nil {
				return out, err
			}
			out += fmt.Sprintf("Commit: file %q is created.\n", pt)
			if !modified {
				modified = true
			}
		}
	}

	if 0 < len(updateFiles) {
		for pt, fi := range updateFiles {
			view, _ := ViewCache.Get(pt)
			viewstr, err := EncodeView(view, cmd.CSV, fi.Delimiter, fi.NoHeader, fi.Encoding, fi.LineBreak)
			if err != nil {
				return out, err
			}

			if err = cmd.UpdateFile(pt, viewstr); err != nil {
				return out, err
			}
			out += fmt.Sprintf("Commit: file %q is updated.\n", pt)
			if !modified {
				modified = true
			}
		}
	}

	ResultSet = []Result{}
	ViewCache.Clear()

	return out, nil
}

func Rollback() string {
	ResultSet = []Result{}
	ViewCache.Clear()

	return "Rolled back.\n"
}

func Select(query parser.SelectQuery) (*View, error) {
	return SelectAsSubquery(query, Filter{})
}

func SelectAsSubquery(query parser.SelectQuery, parentFilter Filter) (*View, error) {
	filter := parentFilter.Copy()

	if query.CommonTableClause != nil {
		if err := filter.CommonTables.Load(query.CommonTableClause.(parser.CommonTableClause)); err != nil {
			return nil, err
		}
	}

	view, err := selectEntity(query.SelectEntity, filter)
	if err != nil {
		return nil, err
	}

	if query.OrderByClause != nil {
		if err := view.OrderBy(query.OrderByClause.(parser.OrderByClause)); err != nil {
			return nil, err
		}
	}

	if query.OffsetClause != nil {
		if err := view.Offset(query.OffsetClause.(parser.OffsetClause)); err != nil {
			return nil, err
		}
	}

	if query.LimitClause != nil {
		if err := view.Limit(query.LimitClause.(parser.LimitClause)); err != nil {
			return nil, err
		}
	}

	view.Fix()

	return view, nil
}

func selectEntity(expr parser.Expression, filter Filter) (*View, error) {
	entity, ok := expr.(parser.SelectEntity)
	if !ok {
		return selectSet(expr.(parser.SelectSet), filter)
	}

	if entity.FromClause == nil {
		entity.FromClause = parser.FromClause{}
	}
	view := NewView()
	err := view.Load(entity.FromClause.(parser.FromClause), filter)
	if err != nil {
		return nil, err
	}

	if entity.WhereClause != nil {
		if err := view.Where(entity.WhereClause.(parser.WhereClause)); err != nil {
			return nil, err
		}
		view.Extract()
	}

	if entity.GroupByClause != nil {
		if err := view.GroupBy(entity.GroupByClause.(parser.GroupByClause)); err != nil {
			return nil, err
		}
	}

	if entity.HavingClause != nil {
		if err := view.Having(entity.HavingClause.(parser.HavingClause)); err != nil {
			return nil, err
		}
		view.Extract()
	}

	if err := view.Select(entity.SelectClause.(parser.SelectClause)); err != nil {
		return nil, err
	}

	return view, nil
}

func selectSetEntity(expr parser.Expression, filter Filter) (*View, error) {
	if subquery, ok := expr.(parser.Subquery); ok {
		return SelectAsSubquery(subquery.Query, filter)
	}

	view, err := selectEntity(expr, filter)
	if err != nil {
		return nil, err
	}
	view.Fix()
	return view, nil
}

func selectSet(set parser.SelectSet, filter Filter) (*View, error) {
	lview, err := selectSetEntity(set.LHS, filter)
	if err != nil {
		return nil, err
	}

	if filter.RecursiveTable.IsRecursive() {
		filter.RecursiveTmpView = nil
		err := selectSetForRecursion(lview, set, filter)
		if err != nil {
			return nil, err
		}
	} else {
		rview, err := selectSetEntity(set.RHS, filter)
		if err != nil {
			return nil, err
		}

		if lview.FieldLen() != rview.FieldLen() {
			return nil, errors.New(fmt.Sprintf("%s: field length does not match", parser.TokenLiteral(set.Operator.Token)))
		}

		switch set.Operator.Token {
		case parser.UNION:
			lview.Union(rview, !set.All.IsEmpty())
		case parser.EXCEPT:
			lview.Except(rview, !set.All.IsEmpty())
		case parser.INTERSECT:
			lview.Intersect(rview, !set.All.IsEmpty())
		}
	}

	lview.SelectAllColumns()

	return lview, nil
}

func selectSetForRecursion(view *View, set parser.SelectSet, filter Filter) error {
	tmpViewName := strings.ToUpper(filter.RecursiveTable.Name.Literal)

	if filter.RecursiveTmpView == nil {
		err := view.UpdateHeader(tmpViewName, filter.RecursiveTable.Columns)
		if err != nil {
			return err
		}
		filter.RecursiveTmpView = view
	}

	rview, err := selectSetEntity(set.RHS, filter)
	if err != nil {
		return err
	}
	if view.FieldLen() != rview.FieldLen() {
		return errors.New(fmt.Sprintf("%s: field length does not match", parser.TokenLiteral(set.Operator.Token)))
	}

	if rview.RecordLen() < 1 {
		return nil
	}
	rview.UpdateHeader(tmpViewName, filter.RecursiveTable.Columns)
	filter.RecursiveTmpView = rview

	switch set.Operator.Token {
	case parser.UNION:
		view.Union(rview, !set.All.IsEmpty())
	case parser.EXCEPT:
		view.Except(rview, !set.All.IsEmpty())
	case parser.INTERSECT:
		view.Intersect(rview, !set.All.IsEmpty())
	}

	return selectSetForRecursion(view, set, filter)
}

func Insert(query parser.InsertQuery) (*View, error) {
	var filter Filter
	if query.CommonTableClause != nil {
		if err := filter.CommonTables.Load(query.CommonTableClause.(parser.CommonTableClause)); err != nil {
			return nil, err
		}
	}

	view := NewView()
	err := view.LoadFromIdentifierWithCommonTables(query.Table, filter)
	if err != nil {
		return nil, err
	}

	fields := query.Fields
	if fields == nil {
		fields = view.Header.TableColumns()
	}

	if query.ValuesList != nil {
		if err := view.InsertValues(fields, query.ValuesList, filter); err != nil {
			return nil, err
		}
	} else {
		if err := view.InsertFromQuery(fields, query.Query.(parser.SelectQuery), filter); err != nil {
			return nil, err
		}
	}

	ViewCache.Replace(view)

	return view, nil
}

func Update(query parser.UpdateQuery) ([]*View, error) {
	var filter Filter
	if query.CommonTableClause != nil {
		if err := filter.CommonTables.Load(query.CommonTableClause.(parser.CommonTableClause)); err != nil {
			return nil, err
		}
	}

	if query.FromClause == nil {
		query.FromClause = parser.FromClause{Tables: query.Tables}
	}

	view := NewView()
	view.UseInternalId = true
	err := view.Load(query.FromClause.(parser.FromClause), filter)
	if err != nil {
		return nil, err
	}

	if query.WhereClause != nil {
		if err := view.Where(query.WhereClause.(parser.WhereClause)); err != nil {
			return nil, err
		}
		view.Extract()
	}

	viewsToUpdate := make(map[string]*View)
	updatedIndices := make(map[string][]int)
	for _, v := range query.Tables {
		table := v.(parser.Table)
		if viewsToUpdate[table.Name()], err = ViewCache.Get(table.Name()); err != nil {
			return nil, err
		}
		viewsToUpdate[table.Name()].UpdateHeader(table.Name(), nil)
		updatedIndices[table.Name()] = []int{}
	}

	filterForLoop := NewFilterForLoop(view, filter)
	for i := range view.Records {
		filterForLoop.Records[0].RecordIndex = i

		for _, v := range query.SetList {
			uset := v.(parser.UpdateSet)

			value, err := filterForLoop.Evaluate(uset.Value)
			if err != nil {
				return nil, err
			}

			viewref, err := view.FieldViewName(uset.Field)
			if err != nil {
				return nil, err
			}
			if _, ok := viewsToUpdate[viewref]; !ok {
				return nil, errors.New(fmt.Sprintf("table %s is not specified in tables to update", viewref))
			}

			internalId, err := view.InternalRecordId(viewref, i)
			if err != nil {
				return nil, errors.New("record to update is ambiguous")
			}

			if InIntSlice(internalId, updatedIndices[viewref]) {
				return nil, errors.New("record to update is ambiguous")
			}

			fieldIdx, _ := viewsToUpdate[viewref].FieldIndex(uset.Field)

			viewsToUpdate[viewref].Records[internalId][fieldIdx] = NewCell(value)
			updatedIndices[viewref] = append(updatedIndices[viewref], internalId)
		}
	}

	views := []*View{}
	for k, v := range viewsToUpdate {
		if err := v.SelectAllColumns(); err != nil {
			return nil, err
		}

		v.Fix()
		v.OperatedRecords = len(updatedIndices[k])

		ViewCache.Replace(v)

		views = append(views, v)
	}

	return views, nil
}

func Delete(query parser.DeleteQuery) ([]*View, error) {
	var filter Filter
	if query.CommonTableClause != nil {
		if err := filter.CommonTables.Load(query.CommonTableClause.(parser.CommonTableClause)); err != nil {
			return nil, err
		}
	}

	fromClause := query.FromClause.(parser.FromClause)
	if query.Tables == nil {
		table := fromClause.Tables[0].(parser.Table)
		if _, ok := table.Object.(parser.Identifier); !ok || 1 < len(fromClause.Tables) {
			return nil, errors.New("update file is not specified")
		}
		query.Tables = []parser.Expression{table}
	}

	view := NewView()
	view.UseInternalId = true
	err := view.Load(query.FromClause.(parser.FromClause), filter)
	if err != nil {
		return nil, err
	}

	if query.WhereClause != nil {
		if err := view.Where(query.WhereClause.(parser.WhereClause)); err != nil {
			return nil, err
		}
		view.Extract()
	}

	viewsToDelete := make(map[string]*View)
	deletedIndices := make(map[string][]int)
	for _, v := range query.Tables {
		table := v.(parser.Table)
		if viewsToDelete[table.Name()], err = ViewCache.Get(table.Name()); err != nil {
			return nil, err
		}
		viewsToDelete[table.Name()].UpdateHeader(table.Name(), nil)
		deletedIndices[table.Name()] = []int{}
	}

	for i := range view.Records {
		for viewref := range viewsToDelete {
			internalId, err := view.InternalRecordId(viewref, i)
			if err != nil {
				continue
			}
			if InIntSlice(internalId, deletedIndices[viewref]) {
				continue
			}
			deletedIndices[viewref] = append(deletedIndices[viewref], internalId)
		}
	}

	views := []*View{}
	for k, v := range viewsToDelete {
		filterdIndices := []int{}
		for i := range v.Records {
			if !InIntSlice(i, deletedIndices[k]) {
				filterdIndices = append(filterdIndices, i)
			}
		}
		v.filteredIndices = filterdIndices
		v.Extract()

		if err := v.SelectAllColumns(); err != nil {
			return nil, err
		}

		v.Fix()
		v.OperatedRecords = len(deletedIndices[k])

		ViewCache.Replace(v)

		views = append(views, v)
	}

	return views, nil
}

func CreateTable(query parser.CreateTable) (*View, error) {
	fields := make([]string, len(query.Fields))
	for i, v := range query.Fields {
		f, _ := v.(parser.Identifier)
		if InStrSlice(f.Literal, fields) {
			return nil, errors.New(fmt.Sprintf("field %s is duplicate", f))
		}
		fields[i] = f.Literal
	}

	flags := cmd.GetFlags()
	fpath := query.Table.Literal
	if !filepath.IsAbs(fpath) {
		fpath = filepath.Join(flags.Repository, fpath)
	}
	delimiter := flags.Delimiter
	if delimiter == cmd.UNDEF {
		if strings.EqualFold(filepath.Ext(fpath), cmd.TSV_EXT) {
			delimiter = '\t'
		} else {
			delimiter = ','
		}
	}

	header := NewHeaderWithoutId(parser.FormatTableName(query.Table.Literal), fields)
	view := &View{
		Header: header,
		FileInfo: &FileInfo{
			Path:      fpath,
			Delimiter: delimiter,
			NoHeader:  false,
			Encoding:  flags.Encoding,
			LineBreak: flags.LineBreak,
		},
	}

	ViewCache.Set(view, parser.FormatTableName(view.FileInfo.Path))

	return view, nil
}

func AddColumns(query parser.AddColumns) (*View, error) {
	if query.Position == nil {
		query.Position = parser.ColumnPosition{
			Position: parser.Token{Token: parser.LAST, Literal: parser.TokenLiteral(parser.LAST)},
		}
	}

	view := NewView()
	err := view.LoadFromIdentifier(query.Table)
	if err != nil {
		return nil, err
	}

	var insertPos int
	pos, _ := query.Position.(parser.ColumnPosition)
	switch pos.Position.Token {
	case parser.FIRST:
		insertPos = 0
	case parser.LAST:
		insertPos = view.FieldLen()
	default:
		idx, err := view.FieldIndex(pos.Column.(parser.FieldReference))
		if err != nil {
			return nil, err
		}
		switch pos.Position.Token {
		case parser.BEFORE:
			insertPos = idx
		default: //parser.AFTER
			insertPos = idx + 1
		}
	}

	columnNames := view.Header.TableColumnNames()
	fields := make([]string, len(query.Columns))
	defaults := make([]parser.Expression, len(query.Columns))
	for i, v := range query.Columns {
		col := v.(parser.ColumnDefault)
		if InStrSlice(col.Column.Literal, columnNames) || InStrSlice(col.Column.Literal, fields) {
			return nil, errors.New(fmt.Sprintf("field %s is duplicate", col.Column))
		}
		fields[i] = col.Column.Literal
		defaults[i] = col.Value
	}
	newFieldLen := view.FieldLen() + len(query.Columns)

	addHeader := NewHeaderWithoutId(parser.FormatTableName(query.Table.Literal), fields)
	header := make(Header, newFieldLen)
	for i, v := range view.Header {
		var idx int
		if i < insertPos {
			idx = i
		} else {
			idx = i + len(fields)
		}
		header[idx] = v
	}
	for i, v := range addHeader {
		header[i+insertPos] = v
	}

	records := make([]Record, view.RecordLen())
	var filter Filter
	filter.Records = append(filter.Records, FilterRecord{
		View: view,
	})
	for i, v := range view.Records {
		record := make(Record, newFieldLen)
		for j, cell := range v {
			var idx int
			if j < insertPos {
				idx = j
			} else {
				idx = j + len(fields)
			}
			record[idx] = cell
		}

		filter.Records[0].RecordIndex = i
		for j, v := range defaults {
			if v == nil {
				v = parser.NewNull()
			}
			val, err := filter.Evaluate(v)
			if err != nil {
				return nil, err
			}
			record[j+insertPos] = NewCell(val)
		}

		records[i] = record
	}

	view.Header = header
	view.Records = records
	view.OperatedFields = len(fields)

	ViewCache.Replace(view)

	return view, nil
}

func DropColumns(query parser.DropColumns) (*View, error) {
	view := NewView()
	err := view.LoadFromIdentifier(query.Table)
	if err != nil {
		return nil, err
	}

	dropIndices := make([]int, len(query.Columns))
	for i, v := range query.Columns {
		idx, err := view.FieldIndex(v.(parser.FieldReference))
		if err != nil {
			return nil, err
		}
		dropIndices[i] = idx
	}

	view.selectFields = []int{}
	for i := 0; i < view.FieldLen(); i++ {
		if view.Header[i].FromTable && !InIntSlice(i, dropIndices) {
			view.selectFields = append(view.selectFields, i)
		}
	}

	view.Fix()
	view.OperatedFields = len(dropIndices)

	ViewCache.Replace(view)

	return view, nil

}

func RenameColumn(query parser.RenameColumn) (*View, error) {
	view := NewView()
	err := view.LoadFromIdentifier(query.Table)
	if err != nil {
		return nil, err
	}

	columnNames := view.Header.TableColumnNames()
	if InStrSlice(query.New.Literal, columnNames) {
		return nil, errors.New(fmt.Sprintf("field %s is duplicate", query.New))
	}

	idx, err := view.FieldIndex(query.Old)
	if err != nil {
		return nil, err
	}

	view.Header[idx].Column = query.New.Literal
	view.OperatedFields = 1

	ViewCache.Replace(view)

	return view, nil
}

func Print(stmt parser.Print) (string, error) {
	var filter Filter
	p, err := filter.Evaluate(stmt.Value)
	if err != nil {
		return "", err
	}
	return p.String(), err
}

func SetFlag(stmt parser.SetFlag) error {
	var err error

	var p parser.Primary

	switch strings.ToUpper(stmt.Name) {
	case "@@DELIMITER", "@@ENCODING", "@@LINE_BREAK", "@@REPOSITORY", "@@DATETIME_FORMAT":
		p = parser.PrimaryToString(stmt.Value)
	case "@@NO_HEADER", "@@WITHOUT_NULL":
		p = parser.PrimaryToBoolean(stmt.Value)
	default:
		return errors.New(fmt.Sprintf("invalid flag name: %s", stmt.Name))
	}
	if parser.IsNull(p) {
		return errors.New(fmt.Sprintf("invalid flag value: %s = %s", stmt.Name, stmt.Value))
	}

	switch strings.ToUpper(stmt.Name) {
	case "@@DELIMITER":
		err = cmd.SetDelimiter(p.(parser.String).Value())
	case "@@ENCODING":
		err = cmd.SetEncoding(p.(parser.String).Value())
	case "@@LINE_BREAK":
		err = cmd.SetLineBreak(p.(parser.String).Value())
	case "@@REPOSITORY":
		err = cmd.SetRepository(p.(parser.String).Value())
	case "@@DATETIME_FORMAT":
		cmd.SetDatetimeFormat(p.(parser.String).Value())
	case "@@NO_HEADER":
		cmd.SetNoHeader(p.(parser.Boolean).Value())
	case "@@WITHOUT_NULL":
		cmd.SetWithoutNull(p.(parser.Boolean).Value())
	}
	return err
}
