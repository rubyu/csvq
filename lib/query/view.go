package query

import (
	"bytes"
	"context"
	gojson "encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/mithrandie/csvq/lib/cmd"
	"github.com/mithrandie/csvq/lib/file"
	"github.com/mithrandie/csvq/lib/json"
	"github.com/mithrandie/csvq/lib/parser"
	"github.com/mithrandie/csvq/lib/value"

	"github.com/mithrandie/go-text"
	"github.com/mithrandie/go-text/csv"
	"github.com/mithrandie/go-text/fixedlen"
	txjson "github.com/mithrandie/go-text/json"
	"github.com/mithrandie/go-text/ltsv"
	"github.com/mithrandie/ternary"
)

type RecordReader interface {
	Read() ([]text.RawText, error)
}

type View struct {
	Header    Header
	RecordSet RecordSet
	FileInfo  *FileInfo

	Tx     *Transaction
	Filter *Filter

	selectFields []int
	selectLabels []string
	isGrouped    bool

	comparisonKeysInEachRecord []string
	sortValuesInEachCell       [][]*SortValue
	sortValuesInEachRecord     []SortValues
	sortDirections             []int
	sortNullPositions          []int

	offset int

	UseInternalId bool
	ForUpdate     bool
}

func NewView(tx *Transaction) *View {
	return &View{
		Tx:            tx,
		UseInternalId: false,
	}
}

func (view *View) Load(ctx context.Context, filter *Filter, clause parser.FromClause) error {
	if clause.Tables == nil {
		var obj parser.QueryExpression
		if cmd.IsReadableFromPipeOrRedirection() {
			obj = parser.Stdin{Stdin: "stdin"}
		} else {
			obj = parser.Dual{}
		}
		clause.Tables = []parser.QueryExpression{parser.Table{Object: obj}}
	}

	views := make([]*View, len(clause.Tables))
	for i, v := range clause.Tables {
		loaded, err := loadView(ctx, filter, v, view.UseInternalId, view.ForUpdate)
		if err != nil {
			return err
		}
		views[i] = loaded
	}

	view.Header = views[0].Header
	view.RecordSet = views[0].RecordSet
	view.FileInfo = views[0].FileInfo

	for i := 1; i < len(views); i++ {
		if err := CrossJoin(ctx, filter, view, views[i]); err != nil {
			return err
		}
	}

	view.Filter = filter
	return nil
}

func (view *View) LoadFromTableIdentifier(ctx context.Context, filter *Filter, table parser.QueryExpression) error {
	fromClause := parser.FromClause{
		Tables: []parser.QueryExpression{
			parser.Table{Object: table},
		},
	}

	return view.Load(ctx, filter, fromClause)
}

func loadView(ctx context.Context, filter *Filter, tableExpr parser.QueryExpression, useInternalId bool, forUpdate bool) (view *View, err error) {
	if parentheses, ok := tableExpr.(parser.Parentheses); ok {
		return loadView(ctx, filter, parentheses.Expr, useInternalId, forUpdate)
	}

	table := tableExpr.(parser.Table)

	switch table.Object.(type) {
	case parser.Dual:
		view = loadDualView(filter.Tx)
	case parser.Stdin:
		fileInfo := &FileInfo{
			Path:               table.Object.String(),
			Format:             filter.Tx.Flags.ImportFormat,
			Delimiter:          filter.Tx.Flags.Delimiter,
			DelimiterPositions: filter.Tx.Flags.DelimiterPositions,
			SingleLine:         filter.Tx.Flags.SingleLine,
			JsonQuery:          filter.Tx.Flags.JsonQuery,
			Encoding:           filter.Tx.Flags.Encoding,
			LineBreak:          filter.Tx.Flags.LineBreak,
			NoHeader:           filter.Tx.Flags.NoHeader,
			EncloseAll:         filter.Tx.Flags.EncloseAll,
			JsonEscape:         filter.Tx.Flags.JsonEscape,
			IsTemporary:        true,
		}

		if !filter.TempViews[len(filter.TempViews)-1].Exists(fileInfo.Path) {
			if !cmd.IsReadableFromPipeOrRedirection() {
				return nil, NewStdinEmptyError(table.Object.(parser.Stdin))
			}

			var loadView *View

			if fileInfo.Format != cmd.JSON {
				buf, err := ioutil.ReadAll(os.Stdin)
				if err != nil {
					return nil, NewReadFileError(table.Object.(parser.Stdin), err.Error())
				}

				br := bytes.NewReader(buf)
				loadView, err = loadViewFromFile(ctx, filter.Tx, br, fileInfo, filter.Tx.Flags.WithoutNull)
				if err != nil {
					return nil, NewDataParsingError(table.Object, fileInfo.Path, err.Error())
				}
			} else {
				fileInfo.Encoding = text.UTF8

				buf, err := ioutil.ReadAll(os.Stdin)
				if err != nil {
					return nil, NewReadFileError(table.Object.(parser.Stdin), err.Error())
				}

				headerLabels, rows, escapeType, err := json.LoadTable(fileInfo.JsonQuery, string(buf))
				if err != nil {
					return nil, NewJsonQueryError(parser.JsonQuery{BaseExpr: table.Object.GetBaseExpr()}, err.Error())
				}

				records := make([]Record, 0, len(rows))
				for _, row := range rows {
					records = append(records, NewRecord(row))
				}

				fileInfo.JsonEscape = escapeType

				loadView = NewView(filter.Tx)
				loadView.Header = NewHeader(parser.FormatTableName(fileInfo.Path), headerLabels)
				loadView.RecordSet = records
				loadView.FileInfo = fileInfo
			}

			loadView.FileInfo.InitialHeader = loadView.Header.Copy()
			loadView.FileInfo.InitialRecordSet = loadView.RecordSet.Copy()
			filter.TempViews[len(filter.TempViews)-1].Set(loadView)
		}
		if err = filter.Aliases.Add(table.Name(), fileInfo.Path); err != nil {
			return nil, err
		}

		pathIdent := parser.Identifier{Literal: table.Object.String()}
		if useInternalId {
			view, _ = filter.TempViews[len(filter.TempViews)-1].GetWithInternalId(ctx, pathIdent, filter.Tx.Flags)
		} else {
			view, _ = filter.TempViews[len(filter.TempViews)-1].Get(pathIdent)
		}
		if !strings.EqualFold(table.Object.String(), table.Name().Literal) {
			if err = view.Header.Update(table.Name().Literal, nil); err != nil {
				return nil, err
			}
		}
	case parser.TableObject:
		tableObject := table.Object.(parser.TableObject)

		importFormat := filter.Tx.Flags.ImportFormat
		delimiter := filter.Tx.Flags.Delimiter
		delimiterPositions := filter.Tx.Flags.DelimiterPositions
		singleLine := filter.Tx.Flags.SingleLine
		jsonQuery := filter.Tx.Flags.JsonQuery
		encoding := filter.Tx.Flags.Encoding
		noHeader := filter.Tx.Flags.NoHeader
		withoutNull := filter.Tx.Flags.WithoutNull

		var felem value.Primary
		if tableObject.FormatElement != nil {
			felem, err = filter.Evaluate(ctx, tableObject.FormatElement)
			if err != nil {
				return nil, err
			}
			felem = value.ToString(felem)
		}

		encodingIdx := 0
		noHeaderIdx := 1
		withoutNullIdx := 2

		switch strings.ToUpper(tableObject.Type.Literal) {
		case cmd.CSV.String():
			if felem == nil {
				return nil, NewTableObjectInvalidArgumentError(tableObject, "delimiter is not specified")
			}
			if value.IsNull(felem) {
				return nil, NewTableObjectInvalidDelimiterError(tableObject, tableObject.FormatElement.String())
			}
			s := cmd.UnescapeString(felem.(value.String).Raw())
			d := []rune(s)
			if 1 != len(d) {
				return nil, NewTableObjectInvalidDelimiterError(tableObject, tableObject.FormatElement.String())
			}
			if 3 < len(tableObject.Args) {
				return nil, NewTableObjectArgumentsLengthError(tableObject, 5)
			}
			delimiter = d[0]
			if delimiter == '\t' {
				importFormat = cmd.TSV
			} else {
				importFormat = cmd.CSV
			}
		case cmd.FIXED.String():
			if felem == nil {
				return nil, NewTableObjectInvalidArgumentError(tableObject, "delimiter positions are not specified")
			}
			if value.IsNull(felem) {
				return nil, NewTableObjectInvalidDelimiterPositionsError(tableObject, tableObject.FormatElement.String())
			}
			s := felem.(value.String).Raw()

			var positions []int
			if !strings.EqualFold("SPACES", s) {
				if strings.HasPrefix(s, "s[") || strings.HasPrefix(s, "S[") {
					singleLine = true
					s = s[1:]
				}
				err = gojson.Unmarshal([]byte(s), &positions)
				if err != nil {
					return nil, NewTableObjectInvalidDelimiterPositionsError(tableObject, tableObject.FormatElement.String())
				}
			}
			if 3 < len(tableObject.Args) {
				return nil, NewTableObjectArgumentsLengthError(tableObject, 5)
			}
			delimiterPositions = positions
			importFormat = cmd.FIXED
		case cmd.JSON.String():
			if felem == nil {
				return nil, NewTableObjectInvalidArgumentError(tableObject, "json query is not specified")
			}
			if value.IsNull(felem) {
				return nil, NewTableObjectInvalidJsonQueryError(tableObject, tableObject.FormatElement.String())
			}
			if 0 < len(tableObject.Args) {
				return nil, NewTableObjectJsonArgumentsLengthError(tableObject, 2)
			}
			jsonQuery = felem.(value.String).Raw()
			importFormat = cmd.JSON
			encoding = text.UTF8
		case cmd.LTSV.String():
			if 2 < len(tableObject.Args) {
				return nil, NewTableObjectJsonArgumentsLengthError(tableObject, 3)
			}
			importFormat = cmd.LTSV
			withoutNullIdx, noHeaderIdx = noHeaderIdx, withoutNullIdx
		default:
			return nil, NewTableObjectInvalidObjectError(tableObject, tableObject.Type.Literal)
		}

		args := make([]value.Primary, 3)
		for i, a := range tableObject.Args {
			if pt, ok := a.(parser.PrimitiveType); ok && value.IsNull(pt.Value) {
				continue
			}

			var p value.Primary = value.NewNull()
			if fr, ok := a.(parser.FieldReference); ok {
				a = parser.NewStringValue(fr.Column.Literal)
			}
			if pv, err := filter.Evaluate(ctx, a); err == nil {
				p = pv
			}

			switch i {
			case encodingIdx:
				v := value.ToString(p)
				if !value.IsNull(v) {
					args[i] = v
				} else {
					return nil, NewTableObjectInvalidArgumentError(tableObject, fmt.Sprintf("cannot be converted as a encoding value: %s", tableObject.Args[encodingIdx].String()))
				}
			case noHeaderIdx:
				v := value.ToBoolean(p)
				if !value.IsNull(v) {
					args[i] = v
				} else {
					return nil, NewTableObjectInvalidArgumentError(tableObject, fmt.Sprintf("cannot be converted as a no-header value: %s", tableObject.Args[noHeaderIdx].String()))
				}
			case withoutNullIdx:
				v := value.ToBoolean(p)
				if !value.IsNull(v) {
					args[i] = v
				} else {
					return nil, NewTableObjectInvalidArgumentError(tableObject, fmt.Sprintf("cannot be converted as a without-null value: %s", tableObject.Args[withoutNullIdx].String()))
				}
			}
		}

		if args[encodingIdx] != nil {
			if encoding, err = cmd.ParseEncoding(args[0].(value.String).Raw()); err != nil {
				return nil, NewTableObjectInvalidArgumentError(tableObject, err.Error())
			}
		}
		if args[noHeaderIdx] != nil {
			noHeader = args[noHeaderIdx].(value.Boolean).Raw()
		}
		if args[withoutNullIdx] != nil {
			withoutNull = args[withoutNullIdx].(value.Boolean).Raw()
		}

		view, err = loadObject(
			ctx,
			table.Object.(parser.TableObject).Path,
			table.Name(),
			filter,
			useInternalId,
			forUpdate,
			importFormat,
			delimiter,
			delimiterPositions,
			singleLine,
			jsonQuery,
			encoding,
			filter.Tx.Flags.LineBreak,
			noHeader,
			filter.Tx.Flags.EncloseAll,
			filter.Tx.Flags.JsonEscape,
			withoutNull,
		)
		if err != nil {
			return nil, err
		}

	case parser.Identifier:
		view, err = loadObject(
			ctx,
			table.Object.(parser.Identifier),
			table.Name(),
			filter,
			useInternalId,
			forUpdate,
			cmd.AutoSelect,
			filter.Tx.Flags.Delimiter,
			filter.Tx.Flags.DelimiterPositions,
			filter.Tx.Flags.SingleLine,
			filter.Tx.Flags.JsonQuery,
			filter.Tx.Flags.Encoding,
			filter.Tx.Flags.LineBreak,
			filter.Tx.Flags.NoHeader,
			filter.Tx.Flags.EncloseAll,
			filter.Tx.Flags.JsonEscape,
			filter.Tx.Flags.WithoutNull,
		)
		if err != nil {
			return nil, err
		}
	case parser.Join:
		join := table.Object.(parser.Join)
		view, err = loadView(ctx, filter, join.Table, useInternalId, forUpdate)
		if err != nil {
			return nil, err
		}
		view2, err := loadView(ctx, filter, join.JoinTable, useInternalId, forUpdate)
		if err != nil {
			return nil, err
		}

		condition, includeFields, excludeFields, err := ParseJoinCondition(join, view, view2)
		if err != nil {
			return nil, err
		}

		joinType := join.JoinType.Token
		if join.JoinType.IsEmpty() {
			if join.Direction.IsEmpty() {
				joinType = parser.INNER
			} else {
				joinType = parser.OUTER
			}
		}

		switch joinType {
		case parser.CROSS:
			if err = CrossJoin(ctx, filter, view, view2); err != nil {
				return nil, err
			}
		case parser.INNER:
			if err = InnerJoin(ctx, filter, view, view2, condition); err != nil {
				return nil, err
			}
		case parser.OUTER:
			if err = OuterJoin(ctx, filter, view, view2, condition, join.Direction.Token); err != nil {
				return nil, err
			}
		}

		includeIndices := make([]int, 0, len(includeFields))
		excludeIndices := make([]int, 0, len(includeFields))
		if includeFields != nil {
			for i := range includeFields {
				idx, _ := view.Header.Contains(includeFields[i])
				includeIndices = append(includeIndices, idx)

				idx, _ = view.Header.Contains(excludeFields[i])
				excludeIndices = append(excludeIndices, idx)
			}

			fieldIndices := make([]int, 0, view.FieldLen())
			header := make(Header, 0, view.FieldLen()-len(excludeIndices))
			for _, idx := range includeIndices {
				view.Header[idx].View = ""
				view.Header[idx].Number = 0
				view.Header[idx].IsJoinColumn = true
				header = append(header, view.Header[idx])
				fieldIndices = append(fieldIndices, idx)
			}
			for i := range view.Header {
				if InIntSlice(i, excludeIndices) || InIntSlice(i, includeIndices) {
					continue
				}
				header = append(header, view.Header[i])
				fieldIndices = append(fieldIndices, i)
			}
			view.Header = header

			if err = NewGoroutineTaskManager(view.RecordLen(), -1, filter.Tx.Flags.CPU).Run(ctx, func(index int) error {
				record := make(Record, len(fieldIndices))
				for i, idx := range fieldIndices {
					record[i] = view.RecordSet[index][idx]
				}
				view.RecordSet[index] = record
				return nil
			}); err != nil {
				return nil, err
			}
		}

	case parser.JsonQuery:
		jsonQuery := table.Object.(parser.JsonQuery)
		alias := table.Name().Literal

		queryValue, err := filter.Evaluate(ctx, jsonQuery.Query)
		if err != nil {
			return nil, err
		}
		queryValue = value.ToString(queryValue)

		if value.IsNull(queryValue) {
			return nil, NewJsonQueryEmptyError(jsonQuery)
		}

		var reader io.Reader

		if jsonPath, ok := jsonQuery.JsonText.(parser.Identifier); ok {
			fpath, err := SearchJsonFilePath(jsonPath, filter.Tx.Flags.Repository)
			if err != nil {
				return nil, err
			}

			h, err := file.NewHandlerForRead(ctx, filter.Tx.FileContainer, fpath, filter.Tx.WaitTimeout, filter.Tx.RetryDelay)
			if err != nil {
				return nil, ConvertFileHandlerError(err, jsonPath, fpath)
			}
			defer func() {
				if e := filter.Tx.FileContainer.Close(h); e != nil {
					err = AppendCompositeError(err, e)
				}
			}()
			reader = h.FileForRead()
		} else {
			jsonTextValue, err := filter.Evaluate(ctx, jsonQuery.JsonText)
			if err != nil {
				return nil, err
			}
			jsonTextValue = value.ToString(jsonTextValue)

			if value.IsNull(jsonTextValue) {
				return nil, NewJsonTableEmptyError(jsonQuery)
			}

			reader = strings.NewReader(jsonTextValue.(value.String).Raw())
		}

		fileInfo := &FileInfo{
			Path:        alias,
			Format:      cmd.JSON,
			JsonQuery:   queryValue.(value.String).Raw(),
			Encoding:    text.UTF8,
			LineBreak:   filter.Tx.Flags.LineBreak,
			IsTemporary: true,
		}

		view, err = loadViewFromJsonFile(filter.Tx, reader, fileInfo)
		if err != nil {
			return nil, NewJsonQueryError(jsonQuery, err.Error())
		}

		if err = filter.Aliases.Add(table.Name(), ""); err != nil {
			return nil, err
		}

	case parser.Subquery:
		subquery := table.Object.(parser.Subquery)
		view, err = Select(ctx, filter, subquery.Query)
		if err != nil {
			return nil, err
		}

		if err = view.Header.Update(table.Name().Literal, nil); err != nil {
			return nil, err
		}

		if err = filter.Aliases.Add(table.Name(), ""); err != nil {
			return nil, err
		}
	}

	return view, err
}

func loadObject(
	ctx context.Context,
	tableIdentifier parser.Identifier,
	tableName parser.Identifier,
	filter *Filter,
	useInternalId bool,
	forUpdate bool,
	importFormat cmd.Format,
	delimiter rune,
	delimiterPositions []int,
	singleLine bool,
	jsonQuery string,
	encoding text.Encoding,
	lineBreak text.LineBreak,
	noHeader bool,
	encloseAll bool,
	jsonEscape txjson.EscapeType,
	withoutNull bool,
) (view *View, err error) {
	if filter.RecursiveTable != nil && strings.EqualFold(tableIdentifier.Literal, filter.RecursiveTable.Name.Literal) && filter.RecursiveTmpView != nil {
		view = filter.RecursiveTmpView
		if !strings.EqualFold(filter.RecursiveTable.Name.Literal, tableName.Literal) {
			if err = view.Header.Update(tableName.Literal, nil); err != nil {
				return nil, err
			}
		}
	} else if it, err := filter.InlineTables.Get(tableIdentifier); err == nil {
		if err = filter.Aliases.Add(tableName, ""); err != nil {
			return nil, err
		}
		view = it
		if tableIdentifier.Literal != tableName.Literal {
			if err = view.Header.Update(tableName.Literal, nil); err != nil {
				return nil, err
			}
		}
	} else {
		var filePath string
		var commonTableName string

		filePath = tableIdentifier.Literal
		if filter.TempViews.Exists(filePath) {
			commonTableName = parser.FormatTableName(filePath)

			pathIdent := parser.Identifier{Literal: filePath}
			if useInternalId {
				view, _ = filter.TempViews.GetWithInternalId(ctx, pathIdent, filter.Tx.Flags)
			} else {
				view, _ = filter.TempViews.Get(pathIdent)
			}
		} else {
			filePath, err = CreateFilePath(tableIdentifier, filter.Tx.Flags.Repository)
			if err != nil {
				return nil, err
			}

			if !filter.Tx.CachedViews.Exists(filePath) || (forUpdate && !filter.Tx.CachedViews[strings.ToUpper(filePath)].ForUpdate) {
				fileInfo, err := NewFileInfo(tableIdentifier, filter.Tx.Flags.Repository, importFormat, delimiter, encoding, filter.Tx.Flags)
				if err != nil {
					return nil, err
				}
				filePath = fileInfo.Path

				fileInfo.DelimiterPositions = delimiterPositions
				fileInfo.SingleLine = singleLine
				fileInfo.JsonQuery = strings.TrimSpace(jsonQuery)
				fileInfo.LineBreak = lineBreak
				fileInfo.NoHeader = noHeader
				fileInfo.EncloseAll = encloseAll
				fileInfo.JsonEscape = jsonEscape

				if !filter.Tx.CachedViews.Exists(fileInfo.Path) || (forUpdate && !filter.Tx.CachedViews[strings.ToUpper(fileInfo.Path)].ForUpdate) {
					if filter.Tx.CachedViews.Exists(fileInfo.Path) {
						fileInfo = filter.Tx.CachedViews[strings.ToUpper(fileInfo.Path)].FileInfo
					}

					if err = filter.Tx.CachedViews.Dispose(filter.Tx.FileContainer, fileInfo.Path); err != nil {
						return nil, err
					}

					var fp *os.File
					if forUpdate {
						h, err := file.NewHandlerForUpdate(ctx, filter.Tx.FileContainer, fileInfo.Path, filter.Tx.WaitTimeout, filter.Tx.RetryDelay)
						if err != nil {
							return nil, ConvertFileHandlerError(err, tableIdentifier, fileInfo.Path)
						}
						fileInfo.Handler = h
						fp = h.FileForRead()
					} else {
						h, err := file.NewHandlerForRead(ctx, filter.Tx.FileContainer, fileInfo.Path, filter.Tx.WaitTimeout, filter.Tx.RetryDelay)
						if err != nil {
							return nil, ConvertFileHandlerError(err, tableIdentifier, fileInfo.Path)
						}
						defer func() {
							if e := filter.Tx.FileContainer.Close(h); e != nil {
								err = AppendCompositeError(err, e)
							}
						}()
						fp = h.FileForRead()
					}

					loadView, err := loadViewFromFile(ctx, filter.Tx, fp, fileInfo, withoutNull)
					if err != nil {
						err = NewDataParsingError(tableIdentifier, fileInfo.Path, err.Error())
						if e := filter.Tx.FileContainer.Close(fileInfo.Handler); e != nil {
							err = AppendCompositeError(err, e)
						}
						return nil, err
					}
					loadView.ForUpdate = forUpdate
					filter.Tx.CachedViews.Set(loadView)
				}
			}
			commonTableName = parser.FormatTableName(filePath)

			pathIdent := parser.Identifier{Literal: filePath}
			if useInternalId {
				view, _ = filter.Tx.CachedViews.GetWithInternalId(ctx, pathIdent, filter.Tx.Flags)
			} else {
				view, _ = filter.Tx.CachedViews.Get(pathIdent)
			}
		}

		if err = filter.Aliases.Add(tableName, filePath); err != nil {
			return nil, err
		}

		if !strings.EqualFold(commonTableName, tableName.Literal) {
			if err = view.Header.Update(tableName.Literal, nil); err != nil {
				return nil, err
			}
		}
	}
	return view, nil
}

func loadViewFromFile(ctx context.Context, tx *Transaction, fp io.ReadSeeker, fileInfo *FileInfo, withoutNull bool) (*View, error) {
	switch fileInfo.Format {
	case cmd.FIXED:
		return loadViewFromFixedLengthTextFile(ctx, tx, fp, fileInfo, withoutNull)
	case cmd.LTSV:
		return loadViewFromLTSVFile(ctx, tx, fp, fileInfo, withoutNull)
	case cmd.JSON:
		return loadViewFromJsonFile(tx, fp, fileInfo)
	}
	return loadViewFromCSVFile(ctx, tx, fp, fileInfo, withoutNull)
}

func loadViewFromFixedLengthTextFile(ctx context.Context, tx *Transaction, fp io.ReadSeeker, fileInfo *FileInfo, withoutNull bool) (*View, error) {
	if enc, err := text.DetectEncoding(fp); err == nil {
		fileInfo.Encoding = enc
	}

	var r io.Reader

	if fileInfo.DelimiterPositions == nil {
		data, err := ioutil.ReadAll(fp)
		if err != nil {
			return nil, err
		}
		br := bytes.NewReader(data)

		d, err := fixedlen.NewDelimiter(br, fileInfo.Encoding)
		if err != nil {
			return nil, err
		}
		d.NoHeader = fileInfo.NoHeader
		d.Encoding = fileInfo.Encoding
		fileInfo.DelimiterPositions, err = d.Delimit()
		if err != nil {
			return nil, err
		}

		if _, err = br.Seek(0, io.SeekStart); err != nil {
			return nil, NewSystemError(err.Error())
		}
		r = br
	} else {
		r = fp
	}

	reader, err := fixedlen.NewReader(r, fileInfo.DelimiterPositions, fileInfo.Encoding)
	if err != nil {
		return nil, err
	}
	reader.WithoutNull = withoutNull
	reader.Encoding = fileInfo.Encoding
	reader.SingleLine = fileInfo.SingleLine

	var header []string
	if !fileInfo.NoHeader && !fileInfo.SingleLine {
		header, err = reader.ReadHeader()
		if err != nil && err != io.EOF {
			return nil, err
		}
	}

	records, err := readRecordSet(ctx, reader)
	if err != nil {
		return nil, err
	}

	if header == nil {
		header = make([]string, len(fileInfo.DelimiterPositions))
		for i := 0; i < len(fileInfo.DelimiterPositions); i++ {
			header[i] = "c" + strconv.Itoa(i+1)
		}
	}

	if reader.DetectedLineBreak != "" {
		fileInfo.LineBreak = reader.DetectedLineBreak
	}

	view := NewView(tx)
	view.Header = NewHeaderWithAutofill(parser.FormatTableName(fileInfo.Path), header)
	view.RecordSet = records
	view.FileInfo = fileInfo
	return view, nil
}

func loadViewFromCSVFile(ctx context.Context, tx *Transaction, fp io.ReadSeeker, fileInfo *FileInfo, withoutNull bool) (*View, error) {
	if enc, err := text.DetectEncoding(fp); err == nil {
		fileInfo.Encoding = enc
	}

	reader, err := csv.NewReader(fp, fileInfo.Encoding)
	if err != nil {
		return nil, err
	}
	reader.Delimiter = fileInfo.Delimiter
	reader.WithoutNull = withoutNull

	var header []string
	if !fileInfo.NoHeader {
		header, err = reader.ReadHeader()
		if err != nil && err != io.EOF {
			return nil, err
		}
	}

	records, err := readRecordSet(ctx, reader)
	if err != nil {
		return nil, err
	}

	if header == nil {
		header = make([]string, reader.FieldsPerRecord)
		for i := 0; i < reader.FieldsPerRecord; i++ {
			header[i] = "c" + strconv.Itoa(i+1)
		}
	}

	if reader.DetectedLineBreak != "" {
		fileInfo.LineBreak = reader.DetectedLineBreak
	}
	fileInfo.EncloseAll = reader.EnclosedAll

	view := NewView(tx)
	view.Header = NewHeader(parser.FormatTableName(fileInfo.Path), header)
	view.RecordSet = records
	view.FileInfo = fileInfo
	return view, nil
}

func loadViewFromLTSVFile(ctx context.Context, tx *Transaction, fp io.ReadSeeker, fileInfo *FileInfo, withoutNull bool) (*View, error) {
	if enc, err := text.DetectEncoding(fp); err == nil {
		fileInfo.Encoding = enc
	}

	reader, err := ltsv.NewReader(fp, fileInfo.Encoding)
	if err != nil {
		return nil, err
	}
	reader.WithoutNull = withoutNull

	records, err := readRecordSet(ctx, reader)
	if err != nil {
		return nil, err
	}

	header := reader.Header.Fields()
	if err = NewGoroutineTaskManager(len(records), -1, tx.Flags.CPU).Run(ctx, func(index int) error {
		for j := len(records[index]); j < len(header); j++ {
			if withoutNull {
				records[index] = append(records[index], NewCell(value.NewString("")))
			} else {
				records[index] = append(records[index], NewCell(value.NewNull()))
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}

	if reader.DetectedLineBreak != "" {
		fileInfo.LineBreak = reader.DetectedLineBreak
	}

	view := NewView(tx)
	view.Header = NewHeader(parser.FormatTableName(fileInfo.Path), header)
	view.RecordSet = records
	view.FileInfo = fileInfo
	return view, nil
}

func readRecordSet(ctx context.Context, reader RecordReader) (RecordSet, error) {
	var err error
	records := make(RecordSet, 0, 1000)
	rowch := make(chan []text.RawText, 1000)
	fieldch := make(chan []value.Primary, 1000)

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		for {
			primaries, ok := <-fieldch
			if !ok {
				break
			}
			records = append(records, NewRecord(primaries))
		}
		wg.Done()
	}()

	wg.Add(1)
	go func() {
		for {
			row, ok := <-rowch
			if !ok {
				break
			}
			fields := make([]value.Primary, len(row))
			for i, v := range row {
				if v == nil {
					fields[i] = value.NewNull()
				} else {
					fields[i] = value.NewString(string(v))
				}
			}
			fieldch <- fields
		}
		close(fieldch)
		wg.Done()
	}()

	wg.Add(1)
	go func() {
		for {
			if ctx.Err() != nil {
				err = NewContextIsDone(ctx.Err().Error())
				break
			}

			record, e := reader.Read()
			if e == io.EOF {
				break
			}
			if e != nil {
				err = e
				break
			}
			rowch <- record
		}
		close(rowch)
		wg.Done()
	}()

	wg.Wait()

	return records, err
}

func loadViewFromJsonFile(tx *Transaction, fp io.Reader, fileInfo *FileInfo) (*View, error) {
	jsonText, err := ioutil.ReadAll(fp)
	if err != nil {
		return nil, err
	}

	headerLabels, rows, escapeType, err := json.LoadTable(fileInfo.JsonQuery, string(jsonText))
	if err != nil {
		return nil, err
	}

	records := make([]Record, 0, len(rows))
	for _, row := range rows {
		records = append(records, NewRecord(row))
	}

	fileInfo.JsonEscape = escapeType

	view := NewView(tx)
	view.Header = NewHeader(parser.FormatTableName(fileInfo.Path), headerLabels)
	view.RecordSet = records
	view.FileInfo = fileInfo
	return view, nil
}

func loadDualView(tx *Transaction) *View {
	view := View{
		Tx:        tx,
		Header:    NewDualHeader(),
		RecordSet: make([]Record, 1),
	}
	view.RecordSet[0] = NewEmptyRecord(1)
	return &view
}

func NewViewFromGroupedRecord(filterRecord FilterRecord) *View {
	view := NewView(filterRecord.View.Tx)
	view.Header = filterRecord.View.Header
	record := filterRecord.View.RecordSet[filterRecord.RecordIndex]

	view.RecordSet = make([]Record, record.GroupLen())
	for i := 0; i < record.GroupLen(); i++ {
		view.RecordSet[i] = make(Record, view.FieldLen())
		for j, cell := range record {
			grpIdx := i
			if cell.Len() < 2 {
				grpIdx = 0
			}
			view.RecordSet[i][j] = NewCell(cell.GroupedValue(grpIdx))
		}
	}

	view.Filter = filterRecord.View.Filter

	return view
}

func (view *View) Where(ctx context.Context, clause parser.WhereClause) error {
	return view.filter(ctx, clause.Filter)
}

func (view *View) filter(ctx context.Context, condition parser.QueryExpression) error {
	results := make([]bool, view.RecordLen())

	err := NewFilterForSequentialEvaluation(view.Filter, view).EvaluateSequentially(ctx, func(f *Filter, rIdx int) error {
		primary, e := f.Evaluate(ctx, condition)
		if e != nil {
			return e
		}

		if primary.Ternary() == ternary.TRUE {
			results[rIdx] = true
		}
		return nil
	}, nil)
	if err != nil {
		return err
	}

	records := make(RecordSet, 0, len(results))
	for i, ok := range results {
		if ok {
			records = append(records, view.RecordSet[i])
		}
	}

	view.RecordSet = make(RecordSet, len(records))
	copy(view.RecordSet, records)
	return nil
}

func (view *View) GroupBy(ctx context.Context, clause parser.GroupByClause) error {
	return view.group(ctx, clause.Items)
}

func (view *View) group(ctx context.Context, items []parser.QueryExpression) error {
	if items == nil {
		return view.groupAll()
	}

	keys := make([]string, view.RecordLen())

	err := NewFilterForSequentialEvaluation(view.Filter, view).EvaluateSequentially(ctx, func(f *Filter, rIdx int) error {
		values := make([]value.Primary, len(items))
		keyBuf := new(bytes.Buffer)

		for i, item := range items {
			p, e := f.Evaluate(ctx, item)
			if e != nil {
				return e
			}
			values[i] = p
		}
		SerializeComparisonKeys(keyBuf, values, view.Tx.Flags)
		keys[rIdx] = keyBuf.String()
		return nil
	}, nil)
	if err != nil {
		return err
	}

	groups := make(map[string][]int)
	groupKeys := make([]string, 0)
	for i, key := range keys {
		if _, ok := groups[key]; ok {
			groups[key] = append(groups[key], i)
		} else {
			groups[key] = []int{i}
			groupKeys = append(groupKeys, key)
		}
	}

	records := make(RecordSet, len(groupKeys))
	for i, groupKey := range groupKeys {
		record := make(Record, view.FieldLen())
		indices := groups[groupKey]

		for j := 0; j < view.FieldLen(); j++ {
			primaries := make([]value.Primary, len(indices))
			for k, idx := range indices {
				primaries[k] = view.RecordSet[idx][j].Value()
			}
			record[j] = NewGroupCell(primaries)
		}

		records[i] = record
	}

	view.RecordSet = records
	view.isGrouped = true
	for _, item := range items {
		switch item.(type) {
		case parser.FieldReference, parser.ColumnNumber:
			idx, _ := view.FieldIndex(item)
			view.Header[idx].IsGroupKey = true
		}
	}
	return nil
}

func (view *View) groupAll() error {
	if 0 < view.RecordLen() {
		records := make(RecordSet, 1)
		record := make(Record, view.FieldLen())
		for i := 0; i < view.FieldLen(); i++ {
			primaries := make([]value.Primary, len(view.RecordSet))
			for j := range view.RecordSet {
				primaries[j] = view.RecordSet[j][i].Value()
			}
			record[i] = NewGroupCell(primaries)
		}
		records[0] = record
		view.RecordSet = records
	}

	view.isGrouped = true
	return nil
}

func (view *View) Having(ctx context.Context, clause parser.HavingClause) error {
	err := view.filter(ctx, clause.Filter)
	if err != nil {
		if _, ok := err.(*NotGroupingRecordsError); ok {
			if err = view.group(ctx, nil); err != nil {
				return err
			}
			if err = view.filter(ctx, clause.Filter); err != nil {
				return err
			}
		} else {
			return err
		}
	}
	return nil
}

func (view *View) Select(ctx context.Context, clause parser.SelectClause) error {
	var parseAllColumns = func(view *View, fields []parser.QueryExpression) []parser.QueryExpression {
		insertIdx := -1

		for i, field := range fields {
			if _, ok := field.(parser.Field).Object.(parser.AllColumns); ok {
				insertIdx = i
				break
			}
		}

		if insertIdx < 0 {
			return fields
		}

		columns := view.Header.TableColumns()
		insertLen := len(columns)
		insert := make([]parser.QueryExpression, insertLen)
		for i, c := range columns {
			insert[i] = parser.Field{
				Object: c,
			}
		}

		list := make([]parser.QueryExpression, len(fields)-1+insertLen)
		for i, field := range fields {
			switch {
			case i == insertIdx:
				continue
			case i < insertIdx:
				list[i] = field
			default:
				list[i+insertLen-1] = field
			}
		}
		for i, field := range insert {
			list[i+insertIdx] = field
		}

		return list
	}

	var evalFields = func(view *View, fields []parser.QueryExpression) error {
		fieldsObjects := make([]parser.QueryExpression, len(fields))
		for i, f := range fields {
			fieldsObjects[i] = f.(parser.Field).Object
		}
		if err := view.ExtendRecordCapacity(ctx, fieldsObjects); err != nil {
			return err
		}

		view.selectFields = make([]int, len(fields))
		view.selectLabels = make([]string, len(fields))
		for i, f := range fields {
			field := f.(parser.Field)
			alias := ""
			if field.Alias != nil {
				alias = field.Alias.(parser.Identifier).Literal
			}
			idx, err := view.evalColumn(ctx, field.Object, alias)
			if err != nil {
				return err
			}
			view.selectFields[i] = idx
			view.selectLabels[i] = field.Name()
		}
		return nil
	}

	fields := parseAllColumns(view, clause.Fields)

	origFieldLen := view.FieldLen()
	err := evalFields(view, fields)
	if err != nil {
		if _, ok := err.(*NotGroupingRecordsError); ok {
			view.Header = view.Header[:origFieldLen]
			if 0 < view.RecordLen() && view.FieldLen() < len(view.RecordSet[0]) {
				for i := range view.RecordSet {
					view.RecordSet[i] = view.RecordSet[i][:origFieldLen]
				}
			}

			if err = view.group(ctx, nil); err != nil {
				return err
			}
			if err = evalFields(view, fields); err != nil {
				return err
			}
		} else {
			return err
		}
	}

	if clause.IsDistinct() {
		if err = view.GenerateComparisonKeys(ctx); err != nil {
			return err
		}
		records := make(RecordSet, 0, view.RecordLen())
		values := make(map[string]bool)
		for i, v := range view.RecordSet {
			if !values[view.comparisonKeysInEachRecord[i]] {
				values[view.comparisonKeysInEachRecord[i]] = true

				record := make(Record, len(view.selectFields))
				for j, idx := range view.selectFields {
					record[j] = v[idx]
				}
				records = append(records, record)
			}
		}

		hfields := NewEmptyHeader(len(view.selectFields))
		for i, idx := range view.selectFields {
			hfields[i] = view.Header[idx]
			view.selectFields[i] = i
		}

		view.Header = hfields
		view.RecordSet = records
		view.comparisonKeysInEachRecord = nil
		view.sortValuesInEachCell = nil
	}

	return nil
}

func (view *View) GenerateComparisonKeys(ctx context.Context) error {
	view.comparisonKeysInEachRecord = make([]string, view.RecordLen())

	return NewGoroutineTaskManager(view.RecordLen(), -1, view.Tx.Flags.CPU).Run(ctx, func(index int) error {
		buf := new(bytes.Buffer)
		if view.selectFields != nil {
			primaries := make([]value.Primary, len(view.selectFields))
			for j, idx := range view.selectFields {
				primaries[j] = view.RecordSet[index][idx].Value()
			}
			SerializeComparisonKeys(buf, primaries, view.Tx.Flags)
		} else {
			view.RecordSet[index].SerializeComparisonKeys(buf, view.Tx.Flags)
		}
		view.comparisonKeysInEachRecord[index] = buf.String()
		return nil
	})
}

func (view *View) SelectAllColumns(ctx context.Context) error {
	selectClause := parser.SelectClause{
		Fields: []parser.QueryExpression{
			parser.Field{Object: parser.AllColumns{}},
		},
	}
	return view.Select(ctx, selectClause)
}

func (view *View) OrderBy(ctx context.Context, clause parser.OrderByClause) error {
	orderValues := make([]parser.QueryExpression, len(clause.Items))
	for i, item := range clause.Items {
		orderValues[i] = item.(parser.OrderItem).Value
	}
	if err := view.ExtendRecordCapacity(ctx, orderValues); err != nil {
		return err
	}

	sortIndices := make([]int, len(clause.Items))
	for i, v := range clause.Items {
		oi := v.(parser.OrderItem)
		idx, err := view.evalColumn(ctx, oi.Value, "")
		if err != nil {
			return err
		}
		sortIndices[i] = idx
	}

	view.sortValuesInEachRecord = make([]SortValues, view.RecordLen())
	view.sortDirections = make([]int, len(clause.Items))
	view.sortNullPositions = make([]int, len(clause.Items))

	for i, v := range clause.Items {
		oi := v.(parser.OrderItem)
		if oi.Direction.IsEmpty() {
			view.sortDirections[i] = parser.ASC
		} else {
			view.sortDirections[i] = oi.Direction.Token
		}

		if oi.Position.IsEmpty() {
			switch view.sortDirections[i] {
			case parser.ASC:
				view.sortNullPositions[i] = parser.FIRST
			default: //parser.DESC
				view.sortNullPositions[i] = parser.LAST
			}
		} else {
			view.sortNullPositions[i] = oi.Position.Token
		}
	}

	if err := NewGoroutineTaskManager(view.RecordLen(), -1, view.Tx.Flags.CPU).Run(ctx, func(index int) error {
		if view.sortValuesInEachCell != nil && view.sortValuesInEachCell[index] == nil {
			view.sortValuesInEachCell[index] = make([]*SortValue, cap(view.RecordSet[index]))
		}

		sortValues := make(SortValues, len(sortIndices))
		for j, idx := range sortIndices {
			if view.sortValuesInEachCell != nil && idx < len(view.sortValuesInEachCell[index]) && view.sortValuesInEachCell[index][idx] != nil {
				sortValues[j] = view.sortValuesInEachCell[index][idx]
			} else {
				sortValues[j] = NewSortValue(view.RecordSet[index][idx].Value(), view.Tx.Flags)
				if view.sortValuesInEachCell != nil && idx < len(view.sortValuesInEachCell[index]) {
					view.sortValuesInEachCell[index][idx] = sortValues[j]
				}
			}
		}
		view.sortValuesInEachRecord[index] = sortValues
		return nil
	}); err != nil {
		return err
	}

	sort.Sort(view)
	return nil
}

func (view *View) additionalColumns(expr parser.QueryExpression) ([]string, error) {
	list := make([]string, 0)

	switch expr.(type) {
	case parser.FieldReference, parser.ColumnNumber:
		return nil, nil
	case parser.Function:
		if udfn, err := view.Filter.Functions.Get(expr, expr.(parser.Function).Name); err == nil {
			if udfn.IsAggregate && !view.isGrouped {
				return nil, NewNotGroupingRecordsError(expr, expr.(parser.Function).Name)
			}
		}
	case parser.AggregateFunction:
		if !view.isGrouped {
			return nil, NewNotGroupingRecordsError(expr, expr.(parser.AggregateFunction).Name)
		}
	case parser.ListFunction:
		if !view.isGrouped {
			return nil, NewNotGroupingRecordsError(expr, expr.(parser.ListFunction).Name)
		}
	case parser.AnalyticFunction:
		fn := expr.(parser.AnalyticFunction)
		pvalues := fn.AnalyticClause.PartitionValues()
		ovalues := []parser.QueryExpression(nil)
		if fn.AnalyticClause.OrderByClause != nil {
			ovalues = fn.AnalyticClause.OrderByClause.(parser.OrderByClause).Items
		}

		if pvalues != nil {
			for _, pvalue := range pvalues {
				columns, err := view.additionalColumns(pvalue)
				if err != nil {
					return nil, err
				}
				for _, s := range columns {
					if !InStrSliceWithCaseInsensitive(s, list) {
						list = append(list, s)
					}
				}
			}
		}
		if ovalues != nil {
			for _, v := range ovalues {
				item := v.(parser.OrderItem)
				columns, err := view.additionalColumns(item.Value)
				if err != nil {
					return nil, err
				}
				for _, s := range columns {
					if !InStrSliceWithCaseInsensitive(s, list) {
						list = append(list, s)
					}
				}
			}
		}
	}

	if _, err := view.Header.ContainsObject(expr); err != nil {
		s := expr.String()
		if !InStrSliceWithCaseInsensitive(s, list) {
			list = append(list, s)
		}
	}

	return list, nil
}

func (view *View) ExtendRecordCapacity(ctx context.Context, exprs []parser.QueryExpression) error {
	additions := make([]string, 0)
	for _, expr := range exprs {
		columns, err := view.additionalColumns(expr)
		if err != nil {
			return err
		}
		for _, s := range columns {
			if !InStrSliceWithCaseInsensitive(s, additions) {
				additions = append(additions, s)
			}
		}
	}

	currentLen := view.FieldLen()
	fieldCap := currentLen + len(additions)

	if 0 < view.RecordLen() && fieldCap <= cap(view.RecordSet[0]) {
		return nil
	}

	return NewGoroutineTaskManager(view.RecordLen(), -1, view.Tx.Flags.CPU).Run(ctx, func(index int) error {
		record := make(Record, currentLen, fieldCap)
		copy(record, view.RecordSet[index])
		view.RecordSet[index] = record
		return nil
	})
}

func (view *View) evalColumn(ctx context.Context, obj parser.QueryExpression, alias string) (idx int, err error) {
	switch obj.(type) {
	case parser.FieldReference, parser.ColumnNumber:
		if idx, err = view.FieldIndex(obj); err != nil {
			return
		}
		if view.isGrouped && view.Header[idx].IsFromTable && !view.Header[idx].IsGroupKey {
			err = NewFieldNotGroupKeyError(obj)
			return
		}
	default:
		idx, err = view.Header.ContainsObject(obj)
		if err != nil {
			err = nil

			if analyticFunction, ok := obj.(parser.AnalyticFunction); ok {
				err = view.evalAnalyticFunction(ctx, analyticFunction)
				if err != nil {
					return
				}
			} else {
				err = NewFilterForSequentialEvaluation(view.Filter, view).EvaluateSequentially(ctx, func(f *Filter, rIdx int) error {
					primary, e := f.Evaluate(ctx, obj)
					if e != nil {
						return e
					}

					view.RecordSet[rIdx] = append(view.RecordSet[rIdx], NewCell(primary))
					return nil
				}, obj)
				if err != nil {
					return
				}
			}
			view.Header, idx = AddHeaderField(view.Header, parser.FormatFieldIdentifier(obj), alias)
		}
	}

	if 0 < len(alias) {
		if !strings.EqualFold(view.Header[idx].Column, alias) && !InStrSliceWithCaseInsensitive(alias, view.Header[idx].Aliases) {
			view.Header[idx].Aliases = append(view.Header[idx].Aliases, alias)
		}
	}

	return
}

func (view *View) evalAnalyticFunction(ctx context.Context, expr parser.AnalyticFunction) error {
	name := strings.ToUpper(expr.Name)
	if _, ok := AggregateFunctions[name]; !ok {
		if _, ok := AnalyticFunctions[name]; !ok {
			if udfn, err := view.Filter.Functions.Get(expr, expr.Name); err != nil || !udfn.IsAggregate {
				return NewFunctionNotExistError(expr, expr.Name)
			}
		}
	}

	var partitionIndices []int
	if expr.AnalyticClause.PartitionClause != nil {
		partitionExprs := expr.AnalyticClause.PartitionValues()

		partitionIndices = make([]int, len(partitionExprs))
		for i, pexpr := range partitionExprs {
			idx, err := view.evalColumn(ctx, pexpr, "")
			if err != nil {
				return err
			}
			partitionIndices[i] = idx
		}
	}

	if view.sortValuesInEachCell == nil {
		view.sortValuesInEachCell = make([][]*SortValue, view.RecordLen())
	}

	if expr.AnalyticClause.OrderByClause != nil {
		err := view.OrderBy(ctx, expr.AnalyticClause.OrderByClause.(parser.OrderByClause))
		if err != nil {
			return err
		}
	}

	err := Analyze(ctx, view, expr, partitionIndices)

	view.sortValuesInEachRecord = nil
	view.sortDirections = nil
	view.sortNullPositions = nil

	return err
}

func (view *View) Offset(ctx context.Context, clause parser.OffsetClause) error {
	val, err := view.Filter.Evaluate(ctx, clause.Value)
	if err != nil {
		return err
	}
	number := value.ToInteger(val)
	if value.IsNull(number) {
		return NewInvalidOffsetNumberError(clause)
	}
	view.offset = int(number.(value.Integer).Raw())
	if view.offset < 0 {
		view.offset = 0
	}

	if view.RecordLen() <= view.offset {
		view.RecordSet = RecordSet{}
	} else {
		view.RecordSet = view.RecordSet[view.offset:]
		records := make(RecordSet, len(view.RecordSet))
		copy(records, view.RecordSet)
		view.RecordSet = records
	}
	return nil
}

func (view *View) Limit(ctx context.Context, clause parser.LimitClause) error {
	val, err := view.Filter.Evaluate(ctx, clause.Value)
	if err != nil {
		return err
	}

	var limit int
	if clause.IsPercentage() {
		number := value.ToFloat(val)
		if value.IsNull(number) {
			return NewInvalidLimitPercentageError(clause)
		}
		percentage := number.(value.Float).Raw()
		if 100 < percentage {
			limit = 100
		} else if percentage < 0 {
			limit = 0
		} else {
			limit = int(math.Ceil(float64(view.RecordLen()+view.offset) * percentage / 100))
		}
	} else {
		number := value.ToInteger(val)
		if value.IsNull(number) {
			return NewInvalidLimitNumberError(clause)
		}
		limit = int(number.(value.Integer).Raw())
		if limit < 0 {
			limit = 0
		}
	}

	if view.RecordLen() <= limit {
		return nil
	}

	if clause.IsWithTies() && view.sortValuesInEachRecord != nil {
		bottomSortValues := view.sortValuesInEachRecord[limit-1]
		for limit < view.RecordLen() {
			if !bottomSortValues.EquivalentTo(view.sortValuesInEachRecord[limit]) {
				break
			}
			limit++
		}
	}

	view.RecordSet = view.RecordSet[:limit]
	records := make(RecordSet, view.RecordLen())
	copy(records, view.RecordSet)
	view.RecordSet = records
	return nil
}

func (view *View) InsertValues(ctx context.Context, fields []parser.QueryExpression, list []parser.QueryExpression) (int, error) {
	valuesList := make([][]value.Primary, len(list))

	for i, item := range list {
		rv := item.(parser.RowValue)
		values, err := view.Filter.evalRowValue(ctx, rv)
		if err != nil {
			return 0, err
		}
		if len(fields) != len(values) {
			return 0, NewInsertRowValueLengthError(rv, len(fields))
		}

		valuesList[i] = values
	}

	return view.insert(fields, valuesList)
}

func (view *View) InsertFromQuery(ctx context.Context, fields []parser.QueryExpression, query parser.SelectQuery) (int, error) {
	insertView, err := Select(ctx, view.Filter, query)
	if err != nil {
		return 0, err
	}
	if len(fields) != insertView.FieldLen() {
		return 0, NewInsertSelectFieldLengthError(query, len(fields))
	}

	valuesList := make([][]value.Primary, insertView.RecordLen())

	for i, record := range insertView.RecordSet {
		values := make([]value.Primary, insertView.FieldLen())
		for j, cell := range record {
			values[j] = cell.Value()
		}
		valuesList[i] = values
	}

	return view.insert(fields, valuesList)
}

func (view *View) insert(fields []parser.QueryExpression, valuesList [][]value.Primary) (int, error) {
	var valueIndex = func(i int, list []int) int {
		for j, v := range list {
			if i == v {
				return j
			}
		}
		return -1
	}

	var insertRecords int

	fieldIndices, err := view.FieldIndices(fields)
	if err != nil {
		return insertRecords, err
	}

	records := make([]Record, len(valuesList))
	for i, values := range valuesList {
		record := make(Record, view.FieldLen())
		for j := 0; j < view.FieldLen(); j++ {
			idx := valueIndex(j, fieldIndices)
			if idx < 0 {
				record[j] = NewCell(value.NewNull())
			} else {
				record[j] = NewCell(values[idx])
			}
		}
		records[i] = record
	}

	view.RecordSet = append(view.RecordSet, records...)
	return len(valuesList), nil
}

func (view *View) Fix(ctx context.Context) error {
	resize := false
	if len(view.selectFields) < view.FieldLen() {
		resize = true
	} else {
		for i := 0; i < view.FieldLen(); i++ {
			if view.selectFields[i] != i {
				resize = true
				break
			}
		}
	}

	if resize {
		if err := NewGoroutineTaskManager(view.RecordLen(), -1, view.Tx.Flags.CPU).Run(ctx, func(index int) error {
			record := make(Record, len(view.selectFields))
			for j, idx := range view.selectFields {
				if 1 < view.RecordSet[index].GroupLen() {
					record[j] = NewCell(view.RecordSet[index][idx].Value())
				} else {
					record[j] = view.RecordSet[index][idx]
				}
			}
			view.RecordSet[index] = record
			return nil
		}); err != nil {
			return err
		}
	}

	hfields := NewEmptyHeader(len(view.selectFields))

	colNumber := 0
	for i, idx := range view.selectFields {
		colNumber++

		hfields[i] = view.Header[idx]
		hfields[i].Aliases = nil
		hfields[i].Number = colNumber
		hfields[i].IsFromTable = true
		hfields[i].IsJoinColumn = false
		hfields[i].IsGroupKey = false

		if 0 < len(view.selectLabels) {
			hfields[i].Column = view.selectLabels[i]
		}
	}

	view.Header = hfields
	view.Filter = nil
	view.selectFields = nil
	view.selectLabels = nil
	view.isGrouped = false
	view.comparisonKeysInEachRecord = nil
	view.sortValuesInEachCell = nil
	view.sortValuesInEachRecord = nil
	view.sortDirections = nil
	view.sortNullPositions = nil
	view.offset = 0
	return nil
}

func (view *View) Union(ctx context.Context, calcView *View, all bool) (err error) {
	view.RecordSet = append(view.RecordSet, calcView.RecordSet...)
	view.FileInfo = nil

	if !all {
		if err = view.GenerateComparisonKeys(ctx); err != nil {
			return err
		}

		records := make(RecordSet, 0, view.RecordLen())
		values := make(map[string]bool)

		for i, key := range view.comparisonKeysInEachRecord {
			if !values[key] {
				values[key] = true
				records = append(records, view.RecordSet[i])
			}
		}

		view.RecordSet = records
		view.comparisonKeysInEachRecord = nil
	}
	return
}

func (view *View) Except(ctx context.Context, calcView *View, all bool) (err error) {
	if err = view.GenerateComparisonKeys(ctx); err != nil {
		return err
	}
	if err = calcView.GenerateComparisonKeys(ctx); err != nil {
		return err
	}

	keys := make(map[string]bool)
	for _, key := range calcView.comparisonKeysInEachRecord {
		if !keys[key] {
			keys[key] = true
		}
	}

	distinctKeys := make(map[string]bool)
	records := make(RecordSet, 0, view.RecordLen())
	for i, key := range view.comparisonKeysInEachRecord {
		if !keys[key] {
			if !all {
				if distinctKeys[key] {
					continue
				}
				distinctKeys[key] = true
			}
			records = append(records, view.RecordSet[i])
		}
	}
	view.RecordSet = records
	view.FileInfo = nil
	view.comparisonKeysInEachRecord = nil
	return
}

func (view *View) Intersect(ctx context.Context, calcView *View, all bool) (err error) {
	if err = view.GenerateComparisonKeys(ctx); err != nil {
		return err
	}
	if err = calcView.GenerateComparisonKeys(ctx); err != nil {
		return err
	}

	keys := make(map[string]bool)
	for _, key := range calcView.comparisonKeysInEachRecord {
		if !keys[key] {
			keys[key] = true
		}
	}

	distinctKeys := make(map[string]bool)
	records := make(RecordSet, 0, view.RecordLen())
	for i, key := range view.comparisonKeysInEachRecord {
		if _, ok := keys[key]; ok {
			if !all {
				if distinctKeys[key] {
					continue
				}
				distinctKeys[key] = true
			}
			records = append(records, view.RecordSet[i])
		}
	}
	view.RecordSet = records
	view.FileInfo = nil
	view.comparisonKeysInEachRecord = nil
	return
}

func (view *View) ListValuesForAggregateFunctions(ctx context.Context, expr parser.QueryExpression, arg parser.QueryExpression, distinct bool, filter *Filter) ([]value.Primary, error) {
	list := make([]value.Primary, view.RecordLen())

	err := NewFilterForSequentialEvaluation(filter, view).EvaluateSequentially(ctx, func(f *Filter, rIdx int) error {
		p, e := f.Evaluate(ctx, arg)
		if e != nil {
			if _, ok := e.(*NotGroupingRecordsError); ok {
				e = NewNestedAggregateFunctionsError(expr)
			}
			return e
		}
		list[rIdx] = p
		return nil
	}, arg)
	if err != nil {
		return nil, err
	}

	if distinct {
		list = Distinguish(list, view.Tx.Flags)
	}

	return list, nil
}

func (view *View) RestoreHeaderReferences() error {
	return view.Header.Update(parser.FormatTableName(view.FileInfo.Path), nil)
}

func (view *View) FieldIndex(fieldRef parser.QueryExpression) (int, error) {
	if number, ok := fieldRef.(parser.ColumnNumber); ok {
		return view.Header.ContainsNumber(number)
	}
	return view.Header.Contains(fieldRef.(parser.FieldReference))
}

func (view *View) FieldIndices(fields []parser.QueryExpression) ([]int, error) {
	indices := make([]int, len(fields))
	for i, v := range fields {
		idx, err := view.FieldIndex(v)
		if err != nil {
			return nil, err
		}
		indices[i] = idx
	}
	return indices, nil
}

func (view *View) FieldViewName(fieldRef parser.QueryExpression) (string, error) {
	idx, err := view.FieldIndex(fieldRef)
	if err != nil {
		return "", err
	}
	return view.Header[idx].View, nil
}

func (view *View) InternalRecordId(ref string, recordIndex int) (int, error) {
	idx, err := view.Header.ContainsInternalId(ref)
	if err != nil {
		return -1, NewInternalRecordIdNotExistError()
	}
	internalId, ok := view.RecordSet[recordIndex][idx].Value().(value.Integer)
	if !ok {
		return -1, NewInternalRecordIdEmptyError()
	}
	return int(internalId.Raw()), nil
}

func (view *View) FieldLen() int {
	return view.Header.Len()
}

func (view *View) RecordLen() int {
	return view.Len()
}

func (view *View) Len() int {
	return len(view.RecordSet)
}

func (view *View) Swap(i, j int) {
	view.RecordSet[i], view.RecordSet[j] = view.RecordSet[j], view.RecordSet[i]
	view.sortValuesInEachRecord[i], view.sortValuesInEachRecord[j] = view.sortValuesInEachRecord[j], view.sortValuesInEachRecord[i]
	if view.sortValuesInEachCell != nil {
		view.sortValuesInEachCell[i], view.sortValuesInEachCell[j] = view.sortValuesInEachCell[j], view.sortValuesInEachCell[i]
	}
}

func (view *View) Less(i, j int) bool {
	return view.sortValuesInEachRecord[i].Less(view.sortValuesInEachRecord[j], view.sortDirections, view.sortNullPositions)
}

func (view *View) Copy() *View {
	header := view.Header.Copy()
	records := view.RecordSet.Copy()

	return &View{
		Header:    header,
		RecordSet: records,
		FileInfo:  view.FileInfo,
		ForUpdate: view.ForUpdate,
	}
}
