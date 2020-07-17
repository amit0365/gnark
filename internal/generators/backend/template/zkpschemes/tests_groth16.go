package zkpschemes

const Groth16Tests = `

import (
	{{ template "import_curve" . }}
	{{ template "import_backend" . }}
	"path/filepath"
	"runtime/debug"
	"testing"
	"strings"


	{{if eq .Curve "BLS377"}}
		groth16_{{toLower .Curve}} "github.com/consensys/gnark/backend/bls377/groth16"
	{{else if eq .Curve "BLS381"}}
		groth16_{{toLower .Curve}} "github.com/consensys/gnark/backend/bls381/groth16"
	{{else if eq .Curve "BN256"}}
		groth16_{{toLower .Curve}} "github.com/consensys/gnark/backend/bn256/groth16"
	{{ else if eq .Curve "BW761"}}
		groth16_{{toLower .Curve}} "github.com/consensys/gnark/backend/bw761/groth16"
	{{end}}

	"github.com/consensys/gnark/encoding/gob"
	"github.com/consensys/gnark/backend"
	"github.com/consensys/gnark/backend/circuits"
	"github.com/consensys/gnark/backend/groth16"
	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gurvy"

	{{if ne .Curve "GENERIC"}}
	"reflect"
	"github.com/stretchr/testify/require"
	{{end}}
)


func TestCircuits(t *testing.T) {
	for name, circuit := range circuits.Circuits {
		t.Run(name, func(t *testing.T) {
			assert := groth16.NewAssert(t)
			r1cs := circuit.R1CS.ToR1CS(curve.ID)
			assert.NotSolved(r1cs, circuit.Bad)
			assert.Solved(r1cs, circuit.Good, nil)
		})
	}
}

func TestParsePublicInput(t *testing.T) {

	expectedNames := [2]string{"data", backend.OneWire}

	inputOneWire := make(map[string]interface{})
	inputOneWire[ backend.OneWire ] = 3
	if _, err := groth16_{{toLower .Curve}}.ParsePublicInput(expectedNames[:], inputOneWire); err == nil {
		t.Fatal("expected ErrMissingAssigment error")
	}

	missingInput := make(map[string]interface{})
	if _, err := groth16_{{toLower .Curve}}.ParsePublicInput(expectedNames[:], missingInput); err == nil {
		t.Fatal("expected ErrMissingAssigment")
	}

	correctInput := make(map[string]interface{})
	correctInput[ "data" ] = 3
	got, err := groth16_{{toLower .Curve}}.ParsePublicInput(expectedNames[:], correctInput)
	if err != nil {
		t.Fatal(err)
	}

	expected := make([]fr.Element, 2)
	expected[0].SetUint64(3).FromMont()
	expected[1].SetUint64(1).FromMont()
	if len(got) != len(expected) {
		t.Fatal("Unexpected length for assignment")
	}
	for i := 0; i < len(got); i++ {
		if !got[i].Equal(&expected[i]) {
			t.Fatal("error public assignment")
		}
	}

}

//--------------------//
//     benches		  //
//--------------------//

func referenceCircuit() (r1cs.R1CS, map[string]interface{}, map[string]interface{}) {
	for name, circuit := range circuits.Circuits {
		if name == "reference_large" {
			r1cs := circuit.R1CS.ToR1CS(curve.ID)
			return r1cs, circuit.Good, circuit.Bad
		}
	}

	panic("reference circuit is not defined")
}

// BenchmarkSetup is a helper to benchmark Setup on a given circuit
func BenchmarkSetup(b *testing.B) {
	r1cs, _, _ := referenceCircuit()
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	var pk groth16_{{toLower .Curve}}.ProvingKey
	var vk groth16_{{toLower .Curve}}.VerifyingKey
	b.ResetTimer()

	b.Run("setup", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			groth16_{{toLower .Curve}}.Setup(r1cs.(*backend_{{toLower .Curve}}.R1CS), &pk, &vk)
		}
	})
}

// BenchmarkProver is a helper to benchmark Prove on a given circuit
// it will run the Setup, reset the benchmark timer and benchmark the prover
func BenchmarkProver(b *testing.B) {
	r1cs, solution, _ := referenceCircuit()
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	var pk groth16_{{toLower .Curve}}.ProvingKey
	groth16_{{toLower .Curve}}.DummySetup(r1cs.(*backend_{{toLower .Curve}}.R1CS), &pk)

	b.ResetTimer()
	b.Run("prover", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = groth16_{{toLower .Curve}}.Prove(r1cs.(*backend_{{toLower .Curve}}.R1CS), &pk, solution)
		}
	})
}

// BenchmarkVerifier is a helper to benchmark Verify on a given circuit
// it will run the Setup, the Prover and reset the benchmark timer and benchmark the verifier
// the provided solution will be filtered to keep only public inputs
func BenchmarkVerifier(b *testing.B) {
	r1cs, solution, _ := referenceCircuit()
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	var pk groth16_{{toLower .Curve}}.ProvingKey
	var vk groth16_{{toLower .Curve}}.VerifyingKey
	groth16_{{toLower .Curve}}.Setup(r1cs.(*backend_{{toLower .Curve}}.R1CS), &pk, &vk)
	proof, err := groth16_{{toLower .Curve}}.Prove(r1cs.(*backend_{{toLower .Curve}}.R1CS), &pk, solution)
	if err != nil {
		panic(err)
	}

	b.ResetTimer()
	b.Run("verifier", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = groth16_{{toLower .Curve}}.Verify(proof, &vk, solution)
		}
	})
}

`