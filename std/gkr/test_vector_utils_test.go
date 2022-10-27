package gkr

import (
	"encoding/json"
	"fmt"
	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark/backend"
	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gnark/std/polynomial"
	"github.com/consensys/gnark/std/sumcheck"
	"github.com/consensys/gnark/test"
	"github.com/stretchr/testify/assert"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// TODO: Replace this with lookup tables
type Map struct {
	keys   []frontend.Variable
	values []frontend.Variable
}

func getDelta(api frontend.API, x frontend.Variable, deltaIndex int, keys []frontend.Variable) frontend.Variable {
	num := frontend.Variable(1)
	den := frontend.Variable(1)

	for i, key := range keys {
		if i != deltaIndex {
			num = api.Mul(num, api.Sub(key, x))
			den = api.Mul(den, api.Sub(key, keys[deltaIndex]))
		}
	}

	return api.Div(num, den)
}

// Get returns garbage if key is not present
func (m Map) Get(api frontend.API, key frontend.Variable) frontend.Variable {
	res := frontend.Variable(0)

	for i := range m.keys {
		deltaI := getDelta(api, key, i, m.keys)
		res = api.Add(res, api.Mul(deltaI, m.values[i]))
	}

	return res
}

// The keys in a DoubleMap must be constant. i.e. known at setup time
type DoubleMap struct {
	keys1  []frontend.Variable
	keys2  []frontend.Variable
	values [][]frontend.Variable
}

// Get is very inefficient. Do not use outside testing
func (m DoubleMap) Get(api frontend.API, key1, key2 frontend.Variable) frontend.Variable {
	deltas1 := make([]frontend.Variable, len(m.keys1))
	deltas2 := make([]frontend.Variable, len(m.keys2))

	for i := range deltas1 {
		deltas1[i] = getDelta(api, key1, i, m.keys1)
	}

	for j := range deltas2 {
		deltas2[j] = getDelta(api, key2, j, m.keys2)
	}

	res := frontend.Variable(0)

	for i := range deltas1 {
		for j := range deltas2 {
			if m.values[i][j] != nil {
				//api.Println(m.keys1[i], m.keys2[j])
				deltaIJ := api.Mul(deltas1[i], deltas2[j], m.values[i][j])
				res = api.Add(res, deltaIJ)
			}
		}
	}

	return res
}

func register[K comparable](m map[K]int, key K) {
	if _, ok := m[key]; !ok {
		m[key] = len(m)
	}
}

func orderKeys[K comparable](order map[K]int) (ordered []K) {
	ordered = make([]K, len(order))
	for k, i := range order {
		ordered[i] = k
	}
	return
}

type HashMap struct {
	single Map
	double DoubleMap
}

func toVariable(v interface{}) frontend.Variable {
	switch vT := v.(type) {
	case float64:
		return int(vT)
	default:
		return v
	}
}

func sliceToVariableSlice(v []interface{}) (varSlice []frontend.Variable) {
	varSlice = make([]frontend.Variable, len(v))
	for i, vI := range v {
		varSlice[i] = toVariable(vI)
	}
	return
}

func ReadMap(in map[string]interface{}) HashMap {
	single := Map{
		keys:   make([]frontend.Variable, 0),
		values: make([]frontend.Variable, 0),
	}

	keys1 := make(map[string]int)
	keys2 := make(map[string]int)

	for k, v := range in {

		kSep := strings.Split(k, ",")
		switch len(kSep) {
		case 1:
			single.keys = append(single.keys, k)
			single.values = append(single.values, toVariable(v))
		case 2:

			register(keys1, kSep[0])
			register(keys2, kSep[1])

		default:
			panic("too many keys")
		}
	}

	vals := make([][]frontend.Variable, len(keys1))
	for i := range vals {
		vals[i] = make([]frontend.Variable, len(keys2))
	}

	for k, v := range in {
		kSep := strings.Split(k, ",")
		if len(kSep) == 2 {
			i1 := keys1[kSep[0]]
			i2 := keys2[kSep[1]]
			vals[i1][i2] = toVariable(v)
		}
	}

	double := DoubleMap{
		keys1:  toVariableSlice(orderKeys(keys1)),
		keys2:  toVariableSlice(orderKeys(keys2)),
		values: vals,
	}

	return HashMap{
		single: single,
		double: double,
	}
}

func getKeys[K comparable, V any](m map[K]V) []K {
	kS := make([]K, len(m))
	i := 0
	for k := range m {
		kS[i] = k
		i++
	}
	return kS
}

func getValuesOrdered[K comparable, V any](m map[K]V, keys []K) []V {
	vS := make([]V, len(keys))
	for i, k := range keys {
		vS[i] = m[k]
	}
	return vS
}

func toVariableSlice[V any](slice []V) (variableSlice []frontend.Variable) {
	variableSlice = make([]frontend.Variable, len(slice))
	for i, v := range slice {
		variableSlice[i] = toVariable(v)
	}
	return
}

func TestHollow(t *testing.T) {
	toHollow := []frontend.Variable{1, 2, 3}
	hollowed := hollow(toHollow)
	assert.Equal(t, 3, len(hollowed))
}

func separateSumcheckProof(proof sumcheck.Proof) (partialSumPolys [][]frontend.Variable, finalEvalProof []frontend.Variable) {
	if proof.FinalEvalProof == nil {
		finalEvalProof = nil
	} else {
		finalEvalProof = proof.FinalEvalProof.([]frontend.Variable)
	}
	partialSumPolys = make([][]frontend.Variable, len(proof.PartialSumPolys))
	for k := range proof.PartialSumPolys {
		partialSumPolys[k] = proof.PartialSumPolys[k]
	}
	return
}

type PrintableSumcheckProof struct {
	FinalEvalProof  interface{}     `json:"finalEvalProof"`
	PartialSumPolys [][]interface{} `json:"partialSumPolys"`
}

func interleaveSumcheckProof(partialSumPolys [][]frontend.Variable, finalEvalProof []frontend.Variable) (proof sumcheck.Proof) {
	proof.PartialSumPolys = make([]polynomial.Polynomial, len(partialSumPolys))
	for k, polyK := range partialSumPolys {
		proof.PartialSumPolys[k] = polyK
	}

	proof.FinalEvalProof = finalEvalProof
	return
}

func unmarshalSumcheckProof(printable PrintableSumcheckProof) (proof sumcheck.Proof) {
	if printable.FinalEvalProof != nil {
		finalEvalSlice := reflect.ValueOf(printable.FinalEvalProof)
		finalEvalProof := make([]frontend.Variable, finalEvalSlice.Len())
		for k := range finalEvalProof {
			finalEvalProof[k] = toVariable(finalEvalSlice.Index(k).Interface())
		}
		proof.FinalEvalProof = finalEvalProof
	} else {
		proof.FinalEvalProof = nil
	}

	proof.PartialSumPolys = make([]polynomial.Polynomial, len(printable.PartialSumPolys))
	for k := range printable.PartialSumPolys {
		proof.PartialSumPolys[k] = toVariableSlice(printable.PartialSumPolys[k])
	}
	return
}

type TestSingleMapCircuit struct {
	M      Map `gnark:"-"`
	Values []frontend.Variable
}

func (c *TestSingleMapCircuit) Define(api frontend.API) error {

	for i, k := range c.M.keys {
		v := c.M.Get(api, k)
		api.AssertIsEqual(v, c.Values[i])
	}

	return nil
}

func TestSingleMap(t *testing.T) {
	m := map[string]interface{}{
		"1": -2,
		"4": 1,
		"6": 7,
	}
	single := ReadMap(m).single

	assignment := TestSingleMapCircuit{
		M:      single,
		Values: single.values,
	}

	circuit := TestSingleMapCircuit{
		M:      single,
		Values: make([]frontend.Variable, len(m)), // Okay to use the same object?
	}

	test.NewAssert(t).ProverSucceeded(&circuit, &assignment, test.WithBackends(backend.GROTH16))
}

type TestDoubleMapCircuit struct {
	M      DoubleMap `gnark:"-"`
	Values []frontend.Variable
	Keys1  []frontend.Variable `gnark:"-"`
	Keys2  []frontend.Variable `gnark:"-"`
}

func (c *TestDoubleMapCircuit) Define(api frontend.API) error {

	for i := range c.Keys1 {
		v := c.M.Get(api, c.Keys1[i], c.Keys2[i])
		api.AssertIsEqual(v, c.Values[i])
	}

	return nil
}

func toMap(keys1, keys2, values []frontend.Variable) map[string]interface{} {
	res := make(map[string]interface{}, len(keys1))
	for i := range keys1 {
		str := strconv.Itoa(keys1[i].(int)) + "," + strconv.Itoa(keys2[i].(int))
		res[str] = values[i].(int)
	}
	return res
}

func TestReadDoubleMap(t *testing.T) {
	keys1 := []frontend.Variable{1, 2}
	keys2 := []frontend.Variable{1, 0}
	values := []frontend.Variable{3, 1}

	for i := 0; i < 100; i++ {
		m := toMap(keys1, keys2, values)
		double := ReadMap(m).double
		valuesOrdered := [][]frontend.Variable{{3, nil}, {nil, 1}}

		assert.True(t, double.keys1[0] == "1" && double.keys1[1] == "2" || double.keys1[0] == "2" && double.keys1[1] == "1")
		assert.True(t, double.keys2[0] == "1" && double.keys2[1] == "0" || double.keys2[0] == "0" && double.keys2[1] == "1")

		if double.keys1[0] != "1" {
			valuesOrdered[0], valuesOrdered[1] = valuesOrdered[1], valuesOrdered[0]
		}

		if double.keys2[0] != "1" {
			valuesOrdered[0][0], valuesOrdered[0][1] = valuesOrdered[0][1], valuesOrdered[0][0]
			valuesOrdered[1][0], valuesOrdered[1][1] = valuesOrdered[1][1], valuesOrdered[1][0]
		}

		assert.True(t, slice2Eq(valuesOrdered, double.values))

	}

}

func slice2Eq(s1, s2 [][]frontend.Variable) bool {
	if len(s1) != len(s2) {
		return false
	}
	for i := range s1 {
		if !sliceEq(s1[i], s2[i]) {
			return false
		}
	}
	return true
}

func sliceEq(s1, s2 []frontend.Variable) bool {
	if len(s1) != len(s2) {
		return false
	}
	for i := range s1 {
		if s1[i] != s2[i] {
			return false
		}
	}
	return true
}

func TestDoubleMap(t *testing.T) {
	keys1 := []frontend.Variable{1, 5, 5, 3}
	keys2 := []frontend.Variable{1, -5, 4, 4}
	values := []frontend.Variable{0, 2, 3, 0}

	m := toMap(keys1, keys2, values)
	double := ReadMap(m).double

	fmt.Println(double)

	assignment := TestDoubleMapCircuit{
		M:      double,
		Values: values,
		Keys1:  keys1,
		Keys2:  keys2,
	}

	circuit := TestDoubleMapCircuit{
		M:      double,
		Keys1:  keys1,
		Keys2:  keys2,
		Values: make([]frontend.Variable, len(m)), // Okay to use the same object?
	}

	test.NewAssert(t).ProverSucceeded(&circuit, &assignment, test.WithBackends(backend.GROTH16))
}

func TestDoubleMapManyTimes(t *testing.T) {
	for i := 0; i < 100; i++ {
		TestDoubleMap(t)
	}
}

var hashCache = make(map[string]HashMap)

func getHash(path string) (HashMap, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return HashMap{}, err
	}
	if h, ok := hashCache[path]; ok {
		return h, nil
	}
	var bytes []byte
	if bytes, err = os.ReadFile(path); err == nil {
		var asMap map[string]interface{}
		if err = json.Unmarshal(bytes, &asMap); err != nil {
			return HashMap{}, err
		}

		res := ReadMap(asMap)
		hashCache[path] = res
		return res, nil

	} else {
		return HashMap{}, err
	}
}

type MapHashTranscript struct {
	hashMap         HashMap
	stateValid      bool
	resultAvailable bool
	state           frontend.Variable
}

func (m HashMap) hash(api frontend.API, x ...frontend.Variable) frontend.Variable {
	switch len(x) {
	case 1:
		return m.single.Get(api, x[0])
	case 2:
		return m.double.Get(api, x[0], x[1])
	default:
		panic("only one or two input allowed")
	}
}

func (m *MapHashTranscript) Update(api frontend.API, x ...frontend.Variable) {
	api.Println("input to update of size ", len(x), ". first input =", x[0])
	if len(x) > 0 {
		for _, xI := range x {

			if m.stateValid {
				m.state = m.hashMap.hash(api, xI, m.state)
			} else {
				m.state = m.hashMap.hash(api, xI)
			}

			m.stateValid = true
		}
	} else { //just hash the state itself
		if !m.stateValid {
			panic("nothing to hash")
		}
		m.state = m.hashMap.hash(api, m.state)
	}
	m.resultAvailable = true
	api.Println("Hash state is now ", m.state)
}

func (m *MapHashTranscript) Next(api frontend.API, x ...frontend.Variable) frontend.Variable {

	if len(x) > 0 || !m.resultAvailable {
		m.Update(api, x...)
	}
	m.resultAvailable = false
	return m.state
}

func (m *MapHashTranscript) NextN(api frontend.API, N int, x ...frontend.Variable) []frontend.Variable {

	if len(x) > 0 {
		m.Update(api, x...)
	}

	res := make([]frontend.Variable, N)

	for n := range res {
		res[n] = m.Next(api)
	}

	return res
}

type TestTranscriptCircuit struct {
	Expected []frontend.Variable
}

func (c *TestTranscriptCircuit) Define(api frontend.API) error {
	hash, err := getHash("test_vectors/resources/hash.json")
	if err != nil {
		return err
	}
	transcript := MapHashTranscript{hashMap: hash}

	got0 := transcript.Next(api, 0)
	got1 := transcript.NextN(api, 2, 1)
	api.AssertIsEqual(got0, c.Expected[0])
	api.AssertIsEqual(got1[0], c.Expected[1])
	api.AssertIsEqual(got1[1], c.Expected[2])
	return nil
}

func TestTranscript(t *testing.T) {

	test.NewAssert(t).ProverSucceeded(
		&TestTranscriptCircuit{Expected: make([]frontend.Variable, 3)},
		&TestTranscriptCircuit{[]frontend.Variable{1, 1, 2}},
		test.WithBackends(backend.GROTH16), test.WithCurves(ecc.BN254),
	)
}

func hollow[K any](x K) K { //TODO: This but with generics or reflection
	switch X := interface{}(x).(type) {
	case []frontend.Variable:
		res := interface{}(make([]frontend.Variable, len(X)))
		return res.(K)
	case [][]frontend.Variable:
		res := make([][]frontend.Variable, len(X))
		for i, xI := range X {
			res[i] = hollow(xI)
		}
		return interface{}(res).(K)
	case [][][]frontend.Variable:
		res := make([][][]frontend.Variable, len(X))
		for i, xI := range X {
			res[i] = hollow(xI)
		}
		return interface{}(res).(K)

	case [][][][]frontend.Variable:
		res := make([][][][]frontend.Variable, len(X))
		for i, xI := range X {
			res[i] = hollow(xI)
		}
		return interface{}(res).(K)
	default:
		panic("cannot hollow out type " + reflect.TypeOf(x).String())
	}
}
