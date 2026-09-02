package domain

import (
	"math"
	"testing"
)

func TestCheckEmbedInputs(t *testing.T) {
	if err := CheckEmbedInputs(nil); err == nil {
		t.Fatal("empty batch")
	}
	if err := CheckEmbedInputs([]string{"  "}); err == nil {
		t.Fatal("blank text")
	}
	over := make([]string, MaxEmbedInputs+1)
	for i := range over {
		over[i] = "a"
	}
	if err := CheckEmbedInputs(over); err == nil {
		t.Fatal("over cap")
	}
	if err := CheckEmbedInputs([]string{"ada prefers tea"}); err != nil {
		t.Fatal(err)
	}
}

func TestCheckEmbeddings(t *testing.T) {
	ok := make([]float32, EmbedDim)
	ok[0] = 1
	if err := CheckEmbeddings(1, [][]float32{ok}); err != nil {
		t.Fatal(err)
	}
	if err := CheckEmbeddings(1, nil); err == nil {
		t.Fatal("count")
	}
	short := [][]float32{{1}}
	if err := CheckEmbeddings(1, short); err == nil {
		t.Fatal("dimension")
	}
	zero := make([]float32, EmbedDim)
	if err := CheckEmbeddings(1, [][]float32{zero}); err == nil {
		t.Fatal("zero norm")
	}
	bad := make([]float32, EmbedDim)
	bad[0] = float32(math.NaN())
	if err := CheckEmbeddings(1, [][]float32{bad}); err == nil {
		t.Fatal("nan")
	}
	inf := make([]float32, EmbedDim)
	inf[0] = float32(math.Inf(1))
	if err := CheckEmbeddings(1, [][]float32{inf}); err == nil {
		t.Fatal("inf")
	}
}
