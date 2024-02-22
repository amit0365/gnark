// Copyright 2020 ConsenSys Software Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Code generated by gnark DO NOT EDIT

package cs

import (
	"errors"
	"fmt"
	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/field/pool"
	"github.com/consensys/gnark/constraint"
	csolver "github.com/consensys/gnark/constraint/solver"
	"github.com/rs/zerolog"
	"math"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
)

// solver represent the state of the solver during a call to System.Solve(...)
type solver struct {
	*system

	// values and solved are index by the wire (variable) id
	values   []fr.Element
	solved   []bool
	nbSolved uint64

	// maps hintID to hint function
	mHintsFunctions map[csolver.HintID]csolver.Hint

	// used to out api.Println
	logger  zerolog.Logger
	nbTasks int

	a, b, c fr.Vector // R1CS solver will compute the a,b,c matrices

	q *big.Int
}

func newSolver(cs *system, witness fr.Vector, opts ...csolver.Option) (*solver, error) {
	// add GKR options to overwrite the placeholder
	if cs.GkrInfo.Is() {
		var gkrData GkrSolvingData
		opts = append(opts,
			csolver.OverrideHint(cs.GkrInfo.SolveHintID, GkrSolveHint(cs.GkrInfo, &gkrData)),
			csolver.OverrideHint(cs.GkrInfo.ProveHintID, GkrProveHint(cs.GkrInfo.HashName, &gkrData)))
	}
	// parse options
	opt, err := csolver.NewConfig(opts...)
	if err != nil {
		return nil, err
	}

	// check witness size
	witnessOffset := 0
	if cs.Type == constraint.SystemR1CS {
		witnessOffset++
	}

	nbWires := len(cs.Public) + len(cs.Secret) + cs.NbInternalVariables
	expectedWitnessSize := len(cs.Public) - witnessOffset + len(cs.Secret)

	if len(witness) != expectedWitnessSize {
		return nil, fmt.Errorf("invalid witness size, got %d, expected %d", len(witness), expectedWitnessSize)
	}

	// check all hints are there
	hintFunctions := opt.HintFunctions

	// hintsDependencies is from compile time; it contains the list of hints the solver **needs**
	var missing []string
	for hintUUID, hintID := range cs.MHintsDependencies {
		if _, ok := hintFunctions[hintUUID]; !ok {
			missing = append(missing, hintID)
		}
	}

	if len(missing) > 0 {
		return nil, fmt.Errorf("solver missing hint(s): %v", missing)
	}

	s := solver{
		system:          cs,
		values:          make([]fr.Element, nbWires),
		solved:          make([]bool, nbWires),
		mHintsFunctions: hintFunctions,
		logger:          opt.Logger,
		nbTasks:         opt.NbTasks,
		q:               cs.Field(),
	}

	// set the witness indexes as solved
	if witnessOffset == 1 {
		s.solved[0] = true // ONE_WIRE
		s.values[0].SetOne()
	}
	copy(s.values[witnessOffset:], witness)
	for i := range witness {
		s.solved[i+witnessOffset] = true
	}

	// keep track of the number of wire instantiations we do, for a post solve sanity check
	// to ensure we instantiated all wires
	s.nbSolved += uint64(len(witness) + witnessOffset)

	if s.Type == constraint.SystemR1CS {
		n := ecc.NextPowerOfTwo(uint64(cs.GetNbConstraints()))
		s.a = make(fr.Vector, cs.GetNbConstraints(), n)
		s.b = make(fr.Vector, cs.GetNbConstraints(), n)
		s.c = make(fr.Vector, cs.GetNbConstraints(), n)
	}

	return &s, nil
}

func (s *solver) set(id int, value fr.Element) {
	if s.solved[id] {
		panic("solving the same wire twice should never happen.")
	}
	s.values[id] = value
	s.solved[id] = true
	atomic.AddUint64(&s.nbSolved, 1)
}

// computeTerm computes coeff*variable
func (s *solver) computeTerm(t constraint.Term) fr.Element {
	cID, vID := t.CoeffID(), t.WireID()

	if t.IsConstant() {
		return s.Coefficients[cID]
	}

	if cID != 0 && !s.solved[vID] {
		panic("computing a term with an unsolved wire")
	}

	switch cID {
	case constraint.CoeffIdZero:
		return fr.Element{}
	case constraint.CoeffIdOne:
		return s.values[vID]
	case constraint.CoeffIdTwo:
		var res fr.Element
		res.Double(&s.values[vID])
		return res
	case constraint.CoeffIdMinusOne:
		var res fr.Element
		res.Neg(&s.values[vID])
		return res
	default:
		var res fr.Element
		res.Mul(&s.Coefficients[cID], &s.values[vID])
		return res
	}
}

// r += (t.coeff*t.value)
// TODO @gbotrel check t.IsConstant on the caller side when necessary
func (s *solver) accumulateInto(t constraint.Term, r *fr.Element) {
	cID := t.CoeffID()
	vID := t.WireID()

	if t.IsConstant() {
		r.Add(r, &s.Coefficients[cID])
		return
	}

	switch cID {
	case constraint.CoeffIdZero:
		return
	case constraint.CoeffIdOne:
		r.Add(r, &s.values[vID])
	case constraint.CoeffIdTwo:
		var res fr.Element
		res.Double(&s.values[vID])
		r.Add(r, &res)
	case constraint.CoeffIdMinusOne:
		r.Sub(r, &s.values[vID])
	default:
		var res fr.Element
		res.Mul(&s.Coefficients[cID], &s.values[vID])
		r.Add(r, &res)
	}
}

// solveWithHint executes a hint and assign the result to its defined outputs.
func (s *solver) solveWithHint(h *constraint.HintMapping) error {
	// ensure hint function was provided
	f, ok := s.mHintsFunctions[h.HintID]
	if !ok {
		return errors.New("missing hint function")
	}

	// tmp IO big int memory
	nbInputs := len(h.Inputs)
	nbOutputs := int(h.OutputRange.End - h.OutputRange.Start)
	inputs := make([]*big.Int, nbInputs)
	outputs := make([]*big.Int, nbOutputs)
	for i := 0; i < nbOutputs; i++ {
		outputs[i] = pool.BigInt.Get()
		outputs[i].SetUint64(0)
	}

	q := pool.BigInt.Get()
	q.Set(s.q)

	for i := 0; i < nbInputs; i++ {
		var v fr.Element
		for _, term := range h.Inputs[i] {
			if term.IsConstant() {
				v.Add(&v, &s.Coefficients[term.CoeffID()])
				continue
			}
			s.accumulateInto(term, &v)
		}
		inputs[i] = pool.BigInt.Get()
		v.BigInt(inputs[i])
	}

	err := f(q, inputs, outputs)

	var v fr.Element
	for i := range outputs {
		v.SetBigInt(outputs[i])
		s.set(int(h.OutputRange.Start)+i, v)
		pool.BigInt.Put(outputs[i])
	}

	for i := range inputs {
		pool.BigInt.Put(inputs[i])
	}

	pool.BigInt.Put(q)

	return err
}

func (s *solver) printLogs(logs []constraint.LogEntry) {
	if s.logger.GetLevel() == zerolog.Disabled {
		return
	}

	for i := 0; i < len(logs); i++ {
		logLine := s.logValue(logs[i])
		s.logger.Debug().Str(zerolog.CallerFieldName, logs[i].Caller).Msg(logLine)
	}
}

const unsolvedVariable = "<unsolved>"

func (s *solver) logValue(log constraint.LogEntry) string {
	var toResolve []interface{}
	var (
		eval         fr.Element
		missingValue bool
	)
	for j := 0; j < len(log.ToResolve); j++ {
		// before eval le

		missingValue = false
		eval.SetZero()

		for _, t := range log.ToResolve[j] {
			// for each term in the linear expression

			cID, vID := t.CoeffID(), t.WireID()
			if t.IsConstant() {
				// just add the constant
				eval.Add(&eval, &s.Coefficients[cID])
				continue
			}

			if !s.solved[vID] {
				missingValue = true
				break // stop the loop we can't evaluate.
			}

			tv := s.computeTerm(t)
			eval.Add(&eval, &tv)
		}

		// after
		if missingValue {
			toResolve = append(toResolve, unsolvedVariable)
		} else {
			// we have to append our accumulator
			toResolve = append(toResolve, eval.String())
		}

	}
	if len(log.Stack) > 0 {
		var sbb strings.Builder
		for _, lID := range log.Stack {
			location := s.SymbolTable.Locations[lID]
			function := s.SymbolTable.Functions[location.FunctionID]

			sbb.WriteString(function.Name)
			sbb.WriteByte('\n')
			sbb.WriteByte('\t')
			sbb.WriteString(function.Filename)
			sbb.WriteByte(':')
			sbb.WriteString(strconv.Itoa(int(location.Line)))
			sbb.WriteByte('\n')
		}
		toResolve = append(toResolve, sbb.String())
	}
	return fmt.Sprintf(log.Format, toResolve...)
}

// divByCoeff sets res = res / t.Coeff
func (solver *solver) divByCoeff(res *fr.Element, cID uint32) {
	switch cID {
	case constraint.CoeffIdOne:
		return
	case constraint.CoeffIdMinusOne:
		res.Neg(res)
	case constraint.CoeffIdZero:
		panic("division by 0")
	default:
		// this is slow, but shouldn't happen as divByCoeff is called to
		// remove the coeff of an unsolved wire
		// but unsolved wires are (in gnark frontend) systematically set with a coeff == 1 or -1
		res.Div(res, &solver.Coefficients[cID])
	}
}

// Implement constraint.Solver
func (s *solver) GetValue(cID, vID uint32) constraint.Element {
	var r constraint.Element
	e := s.computeTerm(constraint.Term{CID: cID, VID: vID})
	copy(r[:], e[:])
	return r
}
func (s *solver) GetCoeff(cID uint32) constraint.Element {
	var r constraint.Element
	copy(r[:], s.Coefficients[cID][:])
	return r
}
func (s *solver) SetValue(vID uint32, f constraint.Element) {
	s.set(int(vID), *(*fr.Element)(f[:]))
}

func (s *solver) IsSolved(vID uint32) bool {
	return s.solved[vID]
}

// Read interprets input calldata as either a LinearExpression (if R1CS) or a Term (if Plonkish),
// evaluates it and return the result and the number of uint32 word read.
func (s *solver) Read(calldata []uint32) (constraint.Element, int) {
	if s.Type == constraint.SystemSparseR1CS {
		if calldata[0] != 1 {
			panic("invalid calldata")
		}
		return s.GetValue(calldata[1], calldata[2]), 3
	}
	var r fr.Element
	n := int(calldata[0])
	j := 1
	for k := 0; k < n; k++ {
		// we read k Terms
		s.accumulateInto(constraint.Term{CID: calldata[j], VID: calldata[j+1]}, &r)
		j += 2
	}

	var ret constraint.Element
	copy(ret[:], r[:])
	return ret, j
}

// processInstruction decodes the instruction and execute blueprint-defined logic.
// an instruction can encode a hint, a custom constraint or a generic constraint.
func (solver *solver) processInstruction(pi constraint.PackedInstruction, scratch *scratch) error {
	// fetch the blueprint
	blueprint := solver.Blueprints[pi.BlueprintID]
	inst := pi.Unpack(&solver.System)
	cID := inst.ConstraintOffset // here we have 1 constraint in the instruction only

	if solver.Type == constraint.SystemR1CS {
		if bc, ok := blueprint.(constraint.BlueprintR1C); ok {
			// TODO @gbotrel we use the solveR1C method for now, having user-defined
			// blueprint for R1CS would require constraint.Solver interface to add methods
			// to set a,b,c since it's more efficient to compute these while we solve.
			bc.DecompressR1C(&scratch.tR1C, inst)
			return solver.solveR1C(cID, &scratch.tR1C)
		}
	}

	// blueprint declared "I know how to solve this."
	if bc, ok := blueprint.(constraint.BlueprintSolvable); ok {
		if err := bc.Solve(solver, inst); err != nil {
			return solver.wrapErrWithDebugInfo(cID, err)
		}
		return nil
	}

	// blueprint encodes a hint, we execute.
	// TODO @gbotrel may be worth it to move hint logic in blueprint "solve"
	if bc, ok := blueprint.(constraint.BlueprintHint); ok {
		bc.DecompressHint(&scratch.tHint, inst)
		return solver.solveWithHint(&scratch.tHint)
	}

	return nil
}

// run runs the solver. it return an error if a constraint is not satisfied or if not all wires
// were instantiated.
func (solver *solver) run() error {
	// minWorkPerCPU is the minimum target number of constraint a task should hold
	// in other words, if a level has less than minWorkPerCPU, it will not be parallelized and executed
	// sequentially without sync.
	const minWorkPerCPU = 50.0 // TODO @gbotrel revisit that with blocks.

	// cs.Levels has a list of levels, where all constraints in a level l(n) are independent
	// and may only have dependencies on previous levels
	// for each constraint
	// we are guaranteed that each R1C contains at most one unsolved wire
	// first we solve the unsolved wire (if any)
	// then we check that the constraint is valid
	// if a[i] * b[i] != c[i]; it means the constraint is not satisfied
	var wg sync.WaitGroup
	chTasks := make(chan []int, solver.nbTasks)
	chError := make(chan error, solver.nbTasks)

	// start a worker pool
	// each worker wait on chTasks
	// a task is a slice of constraint indexes to be solved
	for i := 0; i < solver.nbTasks; i++ {
		go func() {
			var scratch scratch
			for t := range chTasks {
				for _, i := range t {
					if err := solver.processInstruction(solver.Instructions[i], &scratch); err != nil {
						chError <- err
						wg.Done()
						return
					}
				}
				wg.Done()
			}
		}()
	}

	// clean up pool go routines
	defer func() {
		close(chTasks)
		close(chError)
	}()

	var scratch scratch

	// for each level, we push the tasks
	for _, level := range solver.Levels {

		// max CPU to use
		maxCPU := float64(len(level)) / minWorkPerCPU

		if maxCPU <= 1.0 || solver.nbTasks == 1 {
			// we do it sequentially
			for _, i := range level {
				if err := solver.processInstruction(solver.Instructions[i], &scratch); err != nil {
					return err
				}
			}
			continue
		}

		// number of tasks for this level is set to number of CPU
		// but if we don't have enough work for all our CPU, it can be lower.
		nbTasks := solver.nbTasks
		maxTasks := int(math.Ceil(maxCPU))
		if nbTasks > maxTasks {
			nbTasks = maxTasks
		}
		nbIterationsPerCpus := len(level) / nbTasks

		// more CPUs than tasks: a CPU will work on exactly one iteration
		// note: this depends on minWorkPerCPU constant
		if nbIterationsPerCpus < 1 {
			nbIterationsPerCpus = 1
			nbTasks = len(level)
		}

		extraTasks := len(level) - (nbTasks * nbIterationsPerCpus)
		extraTasksOffset := 0

		for i := 0; i < nbTasks; i++ {
			wg.Add(1)
			_start := i*nbIterationsPerCpus + extraTasksOffset
			_end := _start + nbIterationsPerCpus
			if extraTasks > 0 {
				_end++
				extraTasks--
				extraTasksOffset++
			}
			// since we're never pushing more than num CPU tasks
			// we will never be blocked here
			chTasks <- level[_start:_end]
		}

		// wait for the level to be done
		wg.Wait()

		if len(chError) > 0 {
			return <-chError
		}
	}

	if int(solver.nbSolved) != len(solver.values) {
		return errors.New("solver didn't assign a value to all wires")
	}

	return nil
}

// solveR1C compute unsolved wires in the constraint, if any and set the solver accordingly
//
// returns an error if the solver called a hint function that errored
// returns false, nil if there was no wire to solve
// returns true, nil if exactly one wire was solved. In that case, it is redundant to check that
// the constraint is satisfied later.
func (solver *solver) solveR1C(cID uint32, r *constraint.R1C) error {
	a, b, c := &solver.a[cID], &solver.b[cID], &solver.c[cID]

	// the index of the non-zero entry shows if L, R or O has an uninstantiated wire
	// the content is the ID of the wire non instantiated
	var loc uint8

	var termToCompute constraint.Term

	processLExp := func(l constraint.LinearExpression, val *fr.Element, locValue uint8) {
		for _, t := range l {
			vID := t.WireID()

			// wire is already computed, we just accumulate in val
			if solver.solved[vID] {
				solver.accumulateInto(t, val)
				continue
			}

			if loc != 0 {
				panic("found more than one wire to instantiate")
			}
			termToCompute = t
			loc = locValue
		}
	}

	processLExp(r.L, a, 1)
	processLExp(r.R, b, 2)
	processLExp(r.O, c, 3)

	if loc == 0 {
		// there is nothing to solve, may happen if we have an assertion
		// (ie a constraints that doesn't yield any output)
		// or if we solved the unsolved wires with hint functions
		var check fr.Element
		if !check.Mul(a, b).Equal(c) {
			return solver.wrapErrWithDebugInfo(cID, fmt.Errorf("%s ⋅ %s != %s", a.String(), b.String(), c.String()))
		}
		return nil
	}

	// we compute the wire value and instantiate it
	wID := termToCompute.WireID()

	// solver result
	var wire fr.Element

	switch loc {
	case 1:
		if !b.IsZero() {
			wire.Div(c, b).
				Sub(&wire, a)
			a.Add(a, &wire)
		} else {
			// we didn't actually ensure that a * b == c
			var check fr.Element
			if !check.Mul(a, b).Equal(c) {
				return solver.wrapErrWithDebugInfo(cID, fmt.Errorf("%s ⋅ %s != %s", a.String(), b.String(), c.String()))
			}
		}
	case 2:
		if !a.IsZero() {
			wire.Div(c, a).
				Sub(&wire, b)
			b.Add(b, &wire)
		} else {
			var check fr.Element
			if !check.Mul(a, b).Equal(c) {
				return solver.wrapErrWithDebugInfo(cID, fmt.Errorf("%s ⋅ %s != %s", a.String(), b.String(), c.String()))
			}
		}
	case 3:
		wire.Mul(a, b).
			Sub(&wire, c)

		c.Add(c, &wire)
	}

	// wire is the term (coeff * value)
	// but in the solver we want to store the value only
	// note that in gnark frontend, coeff here is always 1 or -1
	solver.divByCoeff(&wire, termToCompute.CID)
	solver.set(wID, wire)

	return nil
}

// UnsatisfiedConstraintError wraps an error with useful metadata on the unsatisfied constraint
type UnsatisfiedConstraintError struct {
	Err       error
	CID       int     // constraint ID
	DebugInfo *string // optional debug info
}

func (r *UnsatisfiedConstraintError) Error() string {
	if r.DebugInfo != nil {
		return fmt.Sprintf("constraint #%d is not satisfied: %s", r.CID, *r.DebugInfo)
	}
	return fmt.Sprintf("constraint #%d is not satisfied: %s", r.CID, r.Err.Error())
}

func (solver *solver) wrapErrWithDebugInfo(cID uint32, err error) *UnsatisfiedConstraintError {
	var debugInfo *string
	if dID, ok := solver.MDebug[int(cID)]; ok {
		debugInfo = new(string)
		*debugInfo = solver.logValue(solver.DebugInfo[dID])
	}
	return &UnsatisfiedConstraintError{CID: int(cID), Err: err, DebugInfo: debugInfo}
}

// temporary variables to avoid memallocs in hotloop
type scratch struct {
	tR1C  constraint.R1C
	tHint constraint.HintMapping
}
