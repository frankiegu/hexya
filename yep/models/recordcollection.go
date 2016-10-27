// Copyright 2016 NDP Systèmes. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package models

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/npiganeau/yep/yep/tools"
	"github.com/npiganeau/yep/yep/tools/logging"
)

// RecordCollection is a generic struct representing several
// records of a model.
type RecordCollection struct {
	mi        *modelInfo
	callStack []*methodLayer
	query     *Query
	env       *Environment
	ids       []int64
	fetched   bool
}

// String returns the string representation of a RecordSet
func (rc RecordCollection) String() string {
	idsStr := make([]string, len(rc.ids))
	for i, id := range rc.ids {
		idsStr[i] = strconv.Itoa(int(id))
		i++
	}
	rsIds := strings.Join(idsStr, ",")
	return fmt.Sprintf("%s(%s)", rc.mi.name, rsIds)
}

// Env returns the RecordSet's Environment
func (rc RecordCollection) Env() Environment {
	res := *rc.env
	return res
}

// ModelName returns the model name of the RecordSet
func (rc RecordCollection) ModelName() string {
	return rc.mi.name
}

// Ids returns the ids of the RecordSet, fetching from db if necessary.
func (rc RecordCollection) Ids() []int64 {
	rSet := rc.Fetch()
	return rSet.ids
}

// create inserts a new record in the database with the given data.
// data can be either a FieldMap or a struct pointer of the same model as rs.
// This function is private and low level. It should not be called directly.
// Instead use rs.Create(), rs.Call("Create") or env.Create()
func (rc RecordCollection) create(data interface{}) RecordCollection {
	fMap := convertInterfaceToFieldMap(data)
	rc.mi.convertValuesToFieldType(&fMap)
	// clean our fMap from ID and non stored fields
	if idl, ok := fMap["id"]; ok && idl.(int64) == 0 {
		delete(fMap, "id")
	}
	if idu, ok := fMap["ID"]; ok && idu.(int64) == 0 {
		delete(fMap, "ID")
	}

	storedFieldMap := filterMapOnStoredFields(rc.mi, fMap)
	// insert in DB
	var createdId int64
	sql, args := rc.query.insertQuery(storedFieldMap)
	rc.env.cr.Get(&createdId, sql, args...)

	rSet := rc.withIds([]int64{createdId})
	// update reverse relation fields
	rSet.updateRelationFields(fMap)
	// compute stored fields
	rSet.updateStoredFields(fMap)
	return rSet
}

// update updates the database with the given data and returns the number of updated rows.
// It panics in case of error.
// This function is private and low level. It should not be called directly.
// Instead use rs.Write() or rs.Call("Write")
func (rc RecordCollection) update(data interface{}, fieldsToUnset ...string) bool {
	fMap := convertInterfaceToFieldMap(data)
	if _, ok := data.(FieldMap); !ok {
		for _, f := range fieldsToUnset {
			if _, exists := fMap[f]; !exists {
				fMap[f] = nil
			}
		}
	}
	rc.mi.convertValuesToFieldType(&fMap)
	// clean our fMap from ID and non stored fields
	delete(fMap, "id")
	delete(fMap, "ID")
	storedFieldMap := filterMapOnStoredFields(rc.mi, fMap)
	// invalidate cache
	// We do it before the actual write on purpose so that we are sure it
	// is invalidated, even in case of error.
	for _, id := range rc.Ids() {
		rc.env.cache.invalidateRecord(rc.mi, id)
	}
	// update DB
	if len(storedFieldMap) > 0 {
		sql, args := rc.query.updateQuery(storedFieldMap)
		rc.env.cr.Execute(sql, args...)
	}
	// write reverse relation fields
	rc.updateRelationFields(fMap)
	// compute stored fields
	rc.updateStoredFields(fMap)
	return true
}

// updateRelationFields updates reverse relations fields of the
// given fMap.
func (rc RecordCollection) updateRelationFields(fMap FieldMap) {
	rSet := rc.Fetch()
	for field, value := range fMap {
		fi := rc.mi.getRelatedFieldInfo(field)
		switch fi.fieldType {
		case tools.One2Many:
		case tools.Rev2One:
		case tools.Many2Many:
			delQuery := fmt.Sprintf(`DELETE FROM %s WHERE %s IN (?)`, fi.m2mRelModel.tableName, fi.m2mOurField.json)
			rc.env.cr.Execute(delQuery, rSet.ids)
			for _, id := range rSet.ids {
				query := fmt.Sprintf(`INSERT INTO %s (%s, %s) VALUES (?, ?)`, fi.m2mRelModel.tableName,
					fi.m2mOurField.json, fi.m2mTheirField.json)
				for _, relId := range value.([]int64) {
					rc.env.cr.Execute(query, id, relId)
				}
			}
		}
	}
}

// delete deletes the database record of this RecordSet and returns the number of deleted rows.
// This function is private and low level. It should not be called directly.
// Instead use rs.Unlink() or rs.Call("Unlink")
func (rc RecordCollection) delete() int64 {
	sql, args := rc.query.deleteQuery()
	res := rc.env.cr.Execute(sql, args...)
	num, _ := res.RowsAffected()
	return num
}

// Filter returns a new RecordSet filtered on records matching the given additional condition.
func (rc RecordCollection) Filter(fieldName, op string, data interface{}) RecordCollection {
	rc.query.cond = rc.query.cond.And(fieldName, op, data)
	return rc
}

// Exclude returns a new RecordSet filtered on records NOT matching the given additional condition.
func (rc RecordCollection) Exclude(fieldName, op string, data interface{}) RecordCollection {
	rc.query.cond = rc.query.cond.AndNot(fieldName, op, data)
	return rc
}

// Search returns a new RecordSet filtering on the current one with the
// additional given Condition
func (rc RecordCollection) Search(cond *Condition) RecordCollection {
	rc.query.cond = rc.query.cond.AndCond(cond)
	return rc
}

// Limit returns a new RecordSet with only the first 'limit' records.
func (rc RecordCollection) Limit(limit int) RecordCollection {
	rc.query.limit = limit
	return rc
}

// Offset returns a new RecordSet with only the records starting at offset
func (rc RecordCollection) Offset(offset int) RecordCollection {
	rc.query.offset = offset
	return rc
}

// OrderBy returns a new RecordSet ordered by the given ORDER BY expressions
func (rc RecordCollection) OrderBy(exprs ...string) RecordCollection {
	rc.query.orders = append(rc.query.orders, exprs...)
	return rc
}

// GroupBy returns a new RecordSet grouped with the given GROUP BY expressions
func (rc RecordCollection) GroupBy(exprs ...string) RecordCollection {
	rc.query.groups = append(rc.query.groups, exprs...)
	return rc
}

// Distinct returns a new RecordSet without duplicates
func (rc RecordCollection) Distinct() RecordCollection {
	rc.query.distinct = true
	return rc
}

// Fetch query the database with the current filter and returns a RecordSet
// with the queries ids. Fetch is lazy and only return ids. Use Load() instead
// if you want to fetch all fields.
func (rc RecordCollection) Fetch() RecordCollection {
	if !rc.fetched && !rc.query.isEmpty() {
		// We do not load empty queries to keep empty record sets empty
		// Call Load instead to load all the records of the table
		return rc.Load("id")
	}
	return rc
}

/*
SearchCount fetch from the database the number of records that match the RecordSet conditions
It panics in case of error
*/
func (rc RecordCollection) SearchCount() int {
	sql, args := rc.query.countQuery()
	var res int
	rc.env.cr.Get(&res, sql, args...)
	return res
}

// Load query all data of the RecordCollection and store in cache.
// fields are the fields to retrieve in the expression format,
// i.e. "User.Profile.Age" or "user_id.profile_id.age".
// If no fields are given, all DB columns of the RecordCollection's
// model are retrieved. Non-DB fields must be explicitly given in
// fields to be retrieved.
func (rc RecordCollection) Load(fields ...string) RecordCollection {
	var results []FieldMap
	if len(fields) == 0 {
		fields = rc.mi.fields.storedFieldNames()
	}
	subFields, substs := rc.substituteRelatedFields(fields)
	dbFields := filterOnDBFields(rc.mi, subFields)
	sql, args := rc.query.selectQuery(dbFields)
	rows := dbQuery(rc.env.cr.tx, sql, args...)
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		line := make(FieldMap)
		err := rc.mi.scanToFieldMap(rows, &line)
		line.SubstituteKeys(substs)
		if err != nil {
			logging.LogAndPanic(log, err.Error(), "model", rc.ModelName(), "fields", fields)
		}
		results = append(results, line)
		rc.env.cache.addRecord(rc.mi, line["id"].(int64), line)
		ids = append(ids, line["id"].(int64))
	}

	rSet := rc.withIds(ids)
	rSet.loadRelationFields(fields)
	return rSet
}

// loadRelationFields loads one2many, many2many and rev2one fields from the given fields
// names in this RecordCollection into the cache. fields of other types given in fields
// are ignored.
func (rc RecordCollection) loadRelationFields(fields []string) {
	for _, id := range rc.ids {
		for _, fieldName := range fields {
			fi := rc.mi.getRelatedFieldInfo(fieldName)
			switch fi.fieldType {
			case tools.One2Many:
				relRC := rc.env.Pool(fi.relatedModelName).Filter(fi.reverseFK, "=", id).Fetch()
				rc.env.cache.addEntry(rc.mi, id, fieldName, relRC.ids)
			case tools.Many2Many:
				query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s = ?`, fi.m2mTheirField.json,
					fi.m2mRelModel.tableName, fi.m2mOurField.json)
				var ids []int64
				rc.env.cr.Select(&ids, query, id)
				rc.env.cache.addEntry(rc.mi, id, fieldName, ids)
			case tools.Rev2One:
				relRC := rc.env.Pool(fi.relatedModelName).Filter(fi.reverseFK, "=", id).Fetch()
				var relID int64
				if len(relRC.ids) > 0 {
					relID = relRC.ids[0]
				}
				rc.env.cache.addEntry(rc.mi, id, fieldName, relID)
			default:
				continue
			}
		}
	}
}

// Get returns the value of the given fieldName for the first record of this RecordCollection.
// It returns the type's zero value if the RecordCollection is empty.
func (rc RecordCollection) Get(fieldName string) interface{} {
	rSet := rc.Fetch()
	fi, ok := rSet.mi.fields.get(fieldName)
	if !ok {
		logging.LogAndPanic(log, "Unknown field in model", "model", rSet.ModelName(), "field", fieldName)
	}
	var res interface{}
	if rSet.IsEmpty() {
		res = reflect.Zero(fi.structField.Type).Interface()
	} else if fi.isComputedField() && !fi.isStored() {
		fMap := make(FieldMap)
		rSet.computeFieldValues(&fMap, fi.json)
		res = fMap[fi.json]
	} else if fi.isRelatedField() && !fi.isStored() {
		if !rSet.env.cache.checkIfInCache(rSet.mi, []int64{rSet.ids[0]}, []string{fi.relatedPath}) {
			rSet.Load(fi.relatedPath)
		}
		res = rSet.env.cache.get(rSet.mi, rSet.ids[0], fi.relatedPath)
	} else {
		if !rSet.env.cache.checkIfInCache(rSet.mi, []int64{rSet.ids[0]}, []string{fi.json}) {
			// If value is not in cache we fetch the whole model to speed up later calls to Get,
			// except for the case of reverse relation fields, where we only load the requested field.
			if fi.fieldType == tools.One2Many || fi.fieldType == tools.Many2Many || fi.fieldType == tools.Rev2One {
				rSet.Load(fieldName)
			} else {
				rSet.Load()
			}
		}
		res = rSet.env.cache.get(rSet.mi, rSet.ids[0], fi.json)
	}
	if fi.isRelationField() {
		switch r := res.(type) {
		case int64:
			res = newRecordCollection(rSet.Env(), fi.relatedModel.name)
			if r != 0 {
				res = res.(RecordCollection).withIds([]int64{r})
			}
		case []int64:
			res = newRecordCollection(rSet.Env(), fi.relatedModel.name).withIds(r)
		}
	}
	return res
}

// Set sets field given by fieldName to the given value. If the RecordSet has several
// Records, all of them will be updated. Each call to Set makes an update query in the
// database. It panics if it is called on an empty RecordSet.
func (rc RecordCollection) Set(fieldName string, value interface{}) {
	rSet := rc.Fetch()
	if rSet.IsEmpty() {
		logging.LogAndPanic(log, "Call to Set on empty RecordSet", "model", rSet.ModelName(), "field", fieldName, "value", value)
	}
	fMap := make(FieldMap)
	fMap[fieldName] = value
	rSet.Call("Write", fMap)
}

// First populates structPtr with a copy of the first Record of the RecordCollection.
// structPtr must a pointer to a struct.
func (rc RecordCollection) First(structPtr interface{}) {
	rSet := rc.Fetch()
	if err := checkStructPtr(structPtr); err != nil {
		logging.LogAndPanic(log, "Invalid structPtr given", "error", err, "model", rSet.ModelName(), "received", structPtr)
	}
	if rSet.IsEmpty() {
		return
	}
	typ := reflect.TypeOf(structPtr).Elem()
	fields := make([]string, typ.NumField())
	for i := 0; i < typ.NumField(); i++ {
		fields[i] = typ.Field(i).Name
	}
	rSet.Load(fields...)
	fMap := rSet.env.cache.getRecord(rSet.ModelName(), rSet.ids[0])
	mapToStruct(rSet, structPtr, fMap)
}

// All Returns a copy of all records of the RecordCollection.
// It returns an empty slice if the RecordSet is empty.
func (rc RecordCollection) All(structSlicePtr interface{}) {
	rSet := rc.Fetch()
	if err := checkStructSlicePtr(structSlicePtr); err != nil {
		logging.LogAndPanic(log, "Invalid structPtr given", "error", err, "model", rSet.ModelName(), "received", structSlicePtr)
	}
	val := reflect.ValueOf(structSlicePtr)
	// sspType is []*struct
	sspType := val.Type().Elem()
	// structType is struct
	structType := sspType.Elem().Elem()
	val.Elem().Set(reflect.MakeSlice(sspType, rSet.Len(), rSet.Len()))
	recs := rSet.Records()
	for i := 0; i < rSet.Len(); i++ {
		fMap := rSet.env.cache.getRecord(rSet.ModelName(), recs[i].ids[0])
		newStructPtr := reflect.New(structType).Interface()
		mapToStruct(rSet, newStructPtr, fMap)
		val.Elem().Index(i).Set(reflect.ValueOf(newStructPtr))
	}
}

// Records returns the slice of RecordCollection singletons that constitute this
// RecordCollection.
func (rc RecordCollection) Records() []RecordCollection {
	rSet := rc.Load()
	res := make([]RecordCollection, rSet.Len())
	for i, id := range rSet.Ids() {
		newRC := newRecordCollection(rSet.Env(), rSet.ModelName())
		res[i] = newRC.withIds([]int64{id})
	}
	return res
}

// EnsureOne panics if rc is not a singleton
func (rc RecordCollection) EnsureOne() {
	if rc.Len() != 1 {
		logging.LogAndPanic(log, "Expected singleton", "model", rc.ModelName(), "received", rc)
	}
}

// IsEmpty returns true if rc is an empty RecordCollection
func (rc RecordCollection) IsEmpty() bool {
	return rc.Len() == 0
}

// Len returns the number of records in this RecordCollection
func (rc RecordCollection) Len() int {
	rSet := rc.Fetch()
	return len(rSet.ids)
}

// Union returns a new RecordCollection that is the union of this RecordCollection
// and the given `other` RecordCollection. The result is guaranteed to be a
// set of unique records.
func (rc RecordCollection) Union(other RecordCollection) RecordCollection {
	if rc.ModelName() != other.ModelName() {
		logging.LogAndPanic(log, "Unable to union RecordCollections of different models", "this", rc.ModelName(),
			"other", other.ModelName())
	}
	thisRC := rc.Fetch()
	otherRC := other.Fetch()
	idMap := make(map[int64]bool)
	for _, id := range thisRC.ids {
		idMap[id] = true
	}
	for _, id := range otherRC.ids {
		idMap[id] = true
	}
	ids := make([]int64, len(idMap))
	i := 0
	for id := range idMap {
		ids[i] = id
		i++
	}
	return newRecordCollection(rc.Env(), rc.ModelName()).withIds(ids)
}

// withIdMap returns a new RecordCollection pointing to the given ids.
// It overrides the current query with ("ID", "in", ids).
func (rc RecordCollection) withIds(ids []int64) RecordCollection {
	rSet := rc
	rSet.ids = ids
	rSet.fetched = true
	if len(ids) > 0 {
		for _, id := range rSet.ids {
			rSet.env.cache.addEntry(rSet.mi, id, "id", id)
		}
		rSet.query.cond = NewCondition().And("ID", "in", ids)
	}
	return rSet
}

var _ RecordSet = RecordCollection{}

// newRecordCollection returns a new empty RecordCollection in the
// given environment for the given modelName
func newRecordCollection(env Environment, modelName string) RecordCollection {
	mi, ok := modelRegistry.get(modelName)
	if !ok {
		logging.LogAndPanic(log, "Unknown model", "model", modelName)
	}
	rc := RecordCollection{
		mi:    mi,
		query: newQuery(),
		env:   &env,
		ids:   make([]int64, 0),
	}
	rc.query.recordSet = &rc
	return rc
}
