package mapping

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/gotomicro/ego/core/elog"
)

type List struct {
	Data []Item `json:"data"`
}

type Item struct {
	Key    string `json:"key"`
	Typ    string `json:"value"`
	Parent string `json:"parent"`
}

func (m *Item) Assemble(withType bool) string {
	if withType {
		return fmt.Sprintf("`%s` %s,", m.Key, fieldReplace(m.Typ))
	}
	return fmt.Sprintf("`%s`,", m.Key)
}

func (m *Item) AssembleJSONAsString() (res string) {
	field := fmt.Sprintf("_log, '%s'", m.Key)
	if m.Parent != "" {
		field = fmt.Sprintf("JSONExtractRaw(_log, '%s'), '%s'", m.Parent, m.Key)
	}
	if strings.Contains(m.Typ, "JSON") {
		// 需要将包含 JSON 类型的数据转换为 string
		return fmt.Sprintf("toString(JSONExtractRaw(%s)) AS `%s`,", field, m.Key)
	}
	if m.Typ == "String" {
		return fmt.Sprintf("JSONExtractString(%s) AS `%s`,", field, m.Key)
	}
	if m.Typ == "Float64" {
		return fmt.Sprintf("JSONExtractFloat(%s) AS `%s`,", field, m.Key)
	}
	if m.Typ == "Bool" {
		return fmt.Sprintf("JSONExtractBool(%s) AS `%s`,", field, m.Key)
	}
	return fmt.Sprintf("JSONExtractRaw(%s) AS `%s`,", field, m.Key)
}

func Handle(req string) (res List, err error) {
	items := make([]Item, 0)
	data := []byte(req)
	// Converted to json string structure type, the need to pay attention to is the json string type;
	var obj = map[string]interface{}{}
	err = json.Unmarshal(data, &obj)
	if err != nil {
		elog.Error("Handle", elog.Any("req", req), elog.Any("err", err.Error()))
		return
	}
	for k, v := range obj {
		typ := fieldTypeJudgment(v)
		if typ == FieldTypeJSON {
			innerItem, errJson := handleJSON(k, v.(map[string]interface{}))
			if errJson != nil {
				return res, errJson
			}
			items = append(items, innerItem...)
		} else {
			items = append(items, Item{
				Key: k,
				Typ: typ,
			})
		}
	}
	return List{Data: items}, nil
}

func handleJSON(p string, req map[string]interface{}) (items []Item, err error) {
	items = make([]Item, 0)
	// Converted to json string structure type, the need to pay attention to is the json string type;
	for k, v := range req {
		items = append(items, Item{
			Key:    k,
			Typ:    fieldTypeJudgment(v),
			Parent: p,
		})
	}
	return
}

func fieldReplace(current string) (after string) {
	if strings.Contains(current, "JSON") {
		return "String"
	}
	return current
}

const (
	FieldTypeJSON = "JSON"
)

// fieldTypeJudgment json -> clickhouse
func fieldTypeJudgment(req interface{}) string {
	var val string
	switch req := req.(type) {
	case string:
		val = "String"
	case []interface{}:
		innerTyp := fieldTypeJudgment(req[0])
		val = "Array(" + innerTyp + ")"
	case map[string]interface{}:
		val = FieldTypeJSON
	case float64:
		val = "Float64"
	case bool:
		val = "Bool"
	default:
		if reflect.TypeOf(req) == nil {
			elog.Warn("fieldTypeJudgment", elog.Any("type", reflect.TypeOf(req)))
			return "unknown"
		}
		return "unknown"
	}
	return val
}
