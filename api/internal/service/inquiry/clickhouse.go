package inquiry

import (
	"database/sql"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"github.com/gotomicro/ego-component/egorm"
	"github.com/gotomicro/ego/core/elog"

	"github.com/shimohq/mogo/api/internal/invoker"
	"github.com/shimohq/mogo/api/pkg/model/db"
	"github.com/shimohq/mogo/api/pkg/model/view"
)

const ignoreKey = "_time_"
const timeCondition = "_time_ >= parseDateTime64BestEffort('%d', 6, 'Asia/Shanghai') AND _time_ < parseDateTime64BestEffort('%d', 6, 'Asia/Shanghai')"
const defaultTimeParse = "parseDateTimeBestEffort(_time_, 'Asia/Shanghai') AS _time_"

var typORM = map[int]string{
	0: "String",
	1: "Int64",
	2: "Float64",
}

var jsonExtractORM = map[int]string{
	0: "JSONExtractString",
	1: "JSONExtractInt",
	2: "JSONExtractFloat",
}

type ClickHouse struct {
	id int
	db *sql.DB
}

func NewClickHouse(db *sql.DB, id int) *ClickHouse {
	if id == 0 {
		panic("clickhouse add err, id is 0")
	}
	return &ClickHouse{
		db: db,
		id: id,
	}
}

func (c *ClickHouse) ID() int {
	return c.id
}

func (c *ClickHouse) genJsonExtractSQL(iid int, database, table string) (string, error) {
	conds := egorm.Conds{}
	conds["table"] = table
	conds["instance_id"] = iid
	conds["database"] = database
	indexes, err := db.IndexList(conds)
	if err != nil {
		return "", err
	}
	var jsonExtractSQL string
	jsonExtractSQL = ","
	for _, obj := range indexes {
		jsonExtractSQL += fmt.Sprintf("%s(log, '%s') AS %s,", jsonExtractORM[obj.Typ], obj.Field, obj.Field)
	}
	jsonExtractSQL = strings.TrimSuffix(jsonExtractSQL, ",")
	return jsonExtractSQL, nil
}

func (c *ClickHouse) whereConditionSQL(current db.View, list []*db.View) (defaultSQL string, currentSQL string, err error) {
	// It is required to obtain all the view parameters under the current table and construct the default and current view query conditions
	for k, viewRow := range list {
		if k == 0 {
			defaultSQL = fmt.Sprintf("JSONHas(log, '%s') = 0", viewRow.Key)
		} else {
			defaultSQL = fmt.Sprintf("%s AND JSONHas(log, '%s') = 0", defaultSQL, viewRow.Key)
		}
	}
	currentSQL = fmt.Sprintf("JSONHas(log, '%s') = 1", current.Key)
	return
}

func (c *ClickHouse) timeParseSQL(v db.View) string {
	if v.IsUseDefaultTime == 1 {
		return defaultTimeParse
	}
	if v.Format == "%s" {
		return fmt.Sprintf("fromUnixTimestamp64Micro(JSONExtractInt(log, '%s'), 'Asia/Shanghai') AS _time_", v.Key)
	}
	return defaultTimeParse
}

// ViewSync
// delete: list need remove current
// update: list need update current
// create: list need add current
func (c *ClickHouse) ViewSync(table db.Table, current db.View, list []*db.View, isAddOrUpdate bool) (dViewSQL, cViewSQL string, err error) {
	timeParseSQL := c.timeParseSQL(current)
	dWhereSQL, cWhereSQL, err := c.whereConditionSQL(current, list)
	if err != nil {
		return
	}
	jsonExtractSQL, err := c.genJsonExtractSQL(table.Iid, table.Database, table.Name)
	if err != nil {
		return
	}
	if dWhereSQL == "" {
		dWhereSQL = "1=1"
	}
	dName := genName(table.Database, table.Name)
	dStreamName := genStreamName(table.Database, table.Name)
	dViewName := genViewName(table.Database, table.Name, "")
	cViewName := genViewName(table.Database, table.Name, current.Key)
	// build view statement
	dViewSQL = fmt.Sprintf(clickhouseViewORM[TableTypeApp], dViewName, dName, timeParseSQL, jsonExtractSQL, dStreamName, dWhereSQL)
	cViewSQL = fmt.Sprintf(clickhouseViewORM[TableTypeApp], cViewName, dName, timeParseSQL, jsonExtractSQL, dStreamName, cWhereSQL)
	elog.Debug("ViewCreate", elog.String("dViewSQL", dViewSQL), elog.String("cViewSQL", cViewSQL))
	// delete default view
	_, err = c.db.Exec(fmt.Sprintf("drop table IF EXISTS %s;", dViewName))
	if err != nil {
		return
	}
	_, err = c.db.Exec(fmt.Sprintf("drop table IF EXISTS %s;", cViewName))
	if err != nil {
		return
	}
	_, err = c.db.Exec(dViewSQL)
	if err != nil {
		return
	}
	if isAddOrUpdate {
		_, err = c.db.Exec(cViewSQL)
		if err != nil {
			return
		}
	}
	return
}

func (c *ClickHouse) Prepare(res view.ReqQuery) (view.ReqQuery, error) {
	if res.Database != "" {
		res.DatabaseTable = fmt.Sprintf("%s.%s", res.Database, res.Table)
	}
	if res.Page <= 0 {
		res.Page = 1
	}
	if res.PageSize <= 0 {
		res.PageSize = 20
	}
	if res.Query == "" {
		res.Query = "1=1"
	}
	if res.ST == 0 {
		res.ST = time.Now().Add(-time.Hour).Unix()
	}
	if res.ET == 0 {
		res.ET = time.Now().Unix()
	}
	var err error
	res.Query, err = queryTransformer(res.Query)
	return res, err
}

// TableDrop data view stream
func (c *ClickHouse) TableDrop(database, table string) (err error) {
	var (
		views []*db.View
	)
	conds := egorm.Conds{}
	conds["iid"] = c.id
	conds["database"] = database
	conds["table"] = table
	views, err = db.ViewList(invoker.Db, conds)
	_, err = c.db.Exec(fmt.Sprintf("drop table IF EXISTS %s;", genViewName(database, table, "")))
	if err != nil {
		return err
	}
	// query all view
	for _, v := range views {
		_, err = c.db.Exec(fmt.Sprintf("drop table IF EXISTS %s;", genViewName(database, table, v.Key)))
		if err != nil {
			return err
		}
	}
	_, err = c.db.Exec(fmt.Sprintf("drop table IF EXISTS %s;", genStreamName(database, table)))
	if err != nil {
		return err
	}
	_, err = c.db.Exec(fmt.Sprintf("drop table IF EXISTS %s.%s;", database, table))
	if err != nil {
		return err
	}
	return nil
}

// TableCreate create default stream data table and view
func (c *ClickHouse) TableCreate(database string, ct view.ReqTableCreate) (dStreamSQL, dDataSQL, dViewSQL string, err error) {
	timeParseSQL := defaultTimeParse
	dWhereSQL := "1=1"
	jsonExtractSQL := ""
	dName := genName(database, ct.TableName)
	dStreamName := genStreamName(database, ct.TableName)
	dViewName := genViewName(database, ct.TableName, "")
	// build view statement
	dStreamSQL = fmt.Sprintf(clickhouseTableStreamORM[ct.Typ], dStreamName, ct.Brokers, ct.Topics, ct.TableName)
	dDataSQL = fmt.Sprintf(clickhouseTableDataORM[ct.Typ], dName, ct.Days)
	dViewSQL = fmt.Sprintf(clickhouseViewORM[ct.Typ], dViewName, dName, timeParseSQL, jsonExtractSQL, dStreamName, dWhereSQL)
	elog.Debug("TableCreate", elog.Any("dStreamSQL", dStreamSQL), elog.Any("dDataSQL", dDataSQL), elog.Any("dViewSQL", dViewSQL))
	_, err = c.db.Exec(dStreamSQL)
	if err != nil {
		return
	}
	_, err = c.db.Exec(dDataSQL)
	if err != nil {
		return
	}
	_, err = c.db.Exec(dViewSQL)
	if err != nil {
		return
	}
	return
}

func (c *ClickHouse) GET(param view.ReqQuery) (res view.RespQuery, err error) {
	// Initialization
	res.Logs = make([]map[string]interface{}, 0)
	res.Keys = make([]string, 0)
	res.Terms = make([][]string, 0)

	res.Logs, err = c.doQuery(c.logsSQL(param))
	if err != nil {
		return
	}
	res.Count = c.Count(param)
	res.Limited = param.PageSize
	// Read the index data
	conds := egorm.Conds{}
	conds["instance_id"] = param.InstanceId
	conds["database"] = param.Database
	conds["table"] = param.Table
	indexes, _ := db.IndexList(conds)
	for _, i := range indexes {
		res.Keys = append(res.Keys, i.Field)
	}
	return
}

func (c *ClickHouse) Count(param view.ReqQuery) (res uint64) {
	sqlCountData, err := c.doQuery(c.countSQL(param))
	if err != nil {
		return
	}
	if len(sqlCountData) > 0 {
		if sqlCountData[0]["count"] != nil {
			switch sqlCountData[0]["count"].(type) {
			case uint64:
				return sqlCountData[0]["count"].(uint64)
			}
		}
	}
	return 0
}

func (c *ClickHouse) GroupBy(param view.ReqQuery) (res map[string]uint64) {
	res = make(map[string]uint64, 0)
	sqlCountData, err := c.doQuery(c.groupBySQL(param))
	if err != nil {
		return
	}
	elog.Debug("ClickHouse", elog.Any("sqlCountData", sqlCountData))
	for _, v := range sqlCountData {
		if v["count"] != nil {
			var key string
			switch v["f"].(type) {
			case string:
				key = v["f"].(string)
			case uint16:
				key = fmt.Sprintf("%d", v["f"].(uint16))
			case int32:
				key = fmt.Sprintf("%d", v["f"].(int32))
			case int64:
				key = fmt.Sprintf("%d", v["f"].(int64))
			case float64:
				key = fmt.Sprintf("%f", v["f"].(float64))
			default:
				elog.Info("GroupBy", elog.Any("type", reflect.TypeOf(v["f"])))
				continue
			}
			res[key] = v["count"].(uint64)
		}
	}
	return
}

func (c *ClickHouse) Tables(database string) (res []string, err error) {
	res = make([]string, 0)
	list, err := c.doQuery(fmt.Sprintf("select table, count(*) as c from system.columns a left join system.tables b on a.table = b.name where a.database = '%s' and a.name = '%s' and a.type = '%s' and b.engine != 'MaterializedView' group by table", database, ignoreKey, "DateTime64(6)"))
	if err != nil {
		return
	}
	for _, row := range list {
		if count, ok := row["c"]; ok {
			if count.(uint64) == 0 {
				continue
			}
		}
		res = append(res, row["table"].(string))
	}
	return
}

func (c *ClickHouse) Databases() (res []view.RespDatabase, err error) {
	instance, _ := db.InstanceInfo(invoker.Db, c.id)
	list, err := c.doQuery(fmt.Sprintf("select database, count(*) as c from system.columns group by database"))
	if err != nil {
		return
	}
	for _, row := range list {
		if count, ok := row["c"]; ok {
			if count.(uint64) == 0 {
				continue
			}
		}
		res = append(res, view.RespDatabase{
			DatabaseName:   row["database"].(string),
			InstanceName:   instance.Name,
			DatasourceType: instance.Datasource,
			InstanceId:     c.id,
		})
	}
	return
}

// IndexUpdate Data table index operation
func (c *ClickHouse) IndexUpdate(param view.ReqCreateIndex, adds map[string]*db.Index, dels map[string]*db.Index, newList map[string]*db.Index) (err error) {
	// step 1 drop
	for _, del := range dels {
		qs := fmt.Sprintf("alter table %s.%s drop column IF EXISTS %s;", param.Database, param.Table, del.Field)
		_, err = c.db.Exec(qs)
		if err != nil {
			return err
		}
	}
	// step 2 add
	for _, add := range adds {
		qs := fmt.Sprintf("ALTER TABLE %s.%s ADD COLUMN IF NOT EXISTS %s Nullable(%s);", param.Database, param.Table, add.Field, typORM[add.Typ])
		_, err = c.db.Exec(qs)
		if err != nil {
			return err
		}
	}
	// step 3 drop view, contains two views, one using ts and the other using _time_
	viewDropSQL := fmt.Sprintf("drop table IF EXISTS %s.%s;", param.Database, param.Table+"_view")
	_, err = c.db.Exec(viewDropSQL)
	if err != nil {
		return err
	}
	viewTsDropSQL := fmt.Sprintf("drop table IF EXISTS %s.%s;", param.Database, param.Table+"_view_ts")
	_, err = c.db.Exec(viewTsDropSQL)
	if err != nil {
		return err
	}
	// step 4 add view
	var jsonExtractSQL string
	jsonExtractSQL = ","
	for _, obj := range newList {
		jsonExtractSQL += fmt.Sprintf("%s(log, '%s') AS %s,", jsonExtractORM[obj.Typ], obj.Field, obj.Field)
	}
	jsonExtractSQL = strings.TrimSuffix(jsonExtractSQL, ",")
	viewCreateSQL := fmt.Sprintf(`CREATE MATERIALIZED VIEW %s.%s TO %s.%s AS
SELECT
parseDateTimeBestEffortOrNull(_time_) AS _time_,
_source_,
_cluster_,
_log_agent_,
_namespace_,
_node_name_,
_node_ip_,
_container_name_,
_pod_name_,
log AS _raw_log_%s
FROM %s.%s where JSONHas(log, 'ts') = 0;`, param.Database, param.Table+"_view", param.Database, param.Table, jsonExtractSQL, param.Database, param.Table+"_stream")
	_, err = c.db.Exec(viewCreateSQL)
	elog.Info("clickhouse", elog.String("step", "SQL"), elog.String("view", viewCreateSQL))
	if err != nil {
		return err
	}
	viewTsCreateSQL := fmt.Sprintf(`CREATE MATERIALIZED VIEW %s.%s TO %s.%s AS
SELECT
fromUnixTimestamp64Milli(JSONExtractInt(log, 'ts')) AS _time_,
_source_,
_cluster_,
_log_agent_,
_namespace_,
_node_name_,
_node_ip_,
_container_name_,
_pod_name_,
log AS _raw_log_%s
FROM %s.%s where JSONHas(log, 'ts') = 1;`, param.Database, param.Table+"_view_ts", param.Database, param.Table, jsonExtractSQL, param.Database, param.Table+"_stream")
	_, err = c.db.Exec(viewTsCreateSQL)
	elog.Info("clickhouse", elog.String("step", "SQL"), elog.String("viewTs", viewTsCreateSQL))
	if err != nil {
		return err
	}
	return nil
}

func (c *ClickHouse) logsSQL(param view.ReqQuery) (sql string) {
	sql = fmt.Sprintf("SELECT * FROM %s WHERE %s AND "+timeCondition+" ORDER BY "+ignoreKey+" DESC LIMIT %d OFFSET %d",
		param.DatabaseTable,
		param.Query,
		param.ST, param.ET,
		param.PageSize, (param.Page-1)*param.PageSize)
	elog.Debug("ClickHouse", elog.Any("step", "logsSQL"), elog.Any("sql", sql))
	return
}

func (c *ClickHouse) countSQL(param view.ReqQuery) (sql string) {
	sql = fmt.Sprintf("SELECT count(*) as count FROM %s WHERE %s AND "+timeCondition,
		param.DatabaseTable,
		param.Query,
		param.ST, param.ET)
	elog.Debug("ClickHouse", elog.Any("step", "countSQL"), elog.Any("sql", sql))
	return
}

func (c *ClickHouse) groupBySQL(param view.ReqQuery) (sql string) {
	sql = fmt.Sprintf("SELECT count(*) as count, %s as f FROM %s WHERE %s AND "+timeCondition+" group by %s",
		param.Field,
		param.DatabaseTable,
		param.Query,
		param.ST, param.ET, param.Field)
	elog.Debug("ClickHouse", elog.Any("step", "groupBySQL"), elog.Any("sql", sql))
	return
}

func (c *ClickHouse) doQuery(sql string) (res []map[string]interface{}, err error) {
	res = make([]map[string]interface{}, 0)
	rows, err := c.db.Query(sql)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()
	cts, _ := rows.ColumnTypes()
	var (
		fields = make([]string, len(cts))
		values = make([]interface{}, len(cts))
	)
	for idx, field := range cts {
		fields[idx] = field.Name()
	}
	for rows.Next() {
		line := make(map[string]interface{}, 0)
		for idx := range values {
			fieldValue := reflect.ValueOf(&values[idx]).Elem()
			values[idx] = fieldValue.Addr().Interface()
		}
		if err = rows.Scan(values...); err != nil {
			log.Fatal(err)
		}
		elog.Debug("ClickHouse", elog.Any("fields", fields), elog.Any("values", values))
		for k, _ := range fields {
			elog.Debug("ClickHouse", elog.Any("fields", fields[k]), elog.Any("values", values[k]))
			line[fields[k]] = values[k]
		}
		res = append(res, line)
	}
	if err = rows.Err(); err != nil {
		log.Fatal(err)
	}
	return
}
