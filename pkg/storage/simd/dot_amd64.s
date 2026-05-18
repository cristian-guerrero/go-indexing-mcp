//go:build amd64

#include "textflag.h"

// func dotAVX2(a, b []float32) float32
//
// Computes dot product using AVX2 + FMA on single-precision floats.
// Unrolls 4x (32 floats per iteration) for ILP.
TEXT ·dotAVX2(SB), NOSPLIT, $0-52
	MOVQ a_base+0(FP), AX
	MOVQ b_base+24(FP), BX
	MOVQ a_len+8(FP), CX

	VXORPS Y0, Y0, Y0
	VXORPS Y1, Y1, Y1
	VXORPS Y2, Y2, Y2
	VXORPS Y3, Y3, Y3

	CMPQ CX, $32
	JL   block8

	MOVQ CX, R8
	SHRQ $5, CX

loop32:
	VMOVUPS (AX), Y4
	VMOVUPS (BX), Y5
	VFMADD231PS Y4, Y5, Y0

	VMOVUPS 32(AX), Y6
	VMOVUPS 32(BX), Y7
	VFMADD231PS Y6, Y7, Y1

	VMOVUPS 64(AX), Y8
	VMOVUPS 64(BX), Y9
	VFMADD231PS Y8, Y9, Y2

	VMOVUPS 96(AX), Y10
	VMOVUPS 96(BX), Y11
	VFMADD231PS Y10, Y11, Y3

	ADDQ $128, AX
	ADDQ $128, BX
	DECQ CX
	JNZ loop32

	VADDPS Y1, Y0, Y0
	VADDPS Y3, Y2, Y2
	VADDPS Y2, Y0, Y0

	MOVQ R8, CX
	ANDQ $31, CX

block8:
	MOVQ CX, DX
	SHRQ $3, DX
	JZ   scalar

loop8:
	VMOVUPS (AX), Y4
	VMOVUPS (BX), Y5
	VFMADD231PS Y4, Y5, Y0

	ADDQ $32, AX
	ADDQ $32, BX
	DECQ DX
	JNZ loop8

scalar:
	ANDQ $7, CX
	JZ   done

scalar_loop:
	MOVSS (AX), X4
	MOVSS (BX), X5
	MULSS X5, X4
	ADDSS X4, X0

	ADDQ $4, AX
	ADDQ $4, BX
	DECQ CX
	JNZ scalar_loop

done:
	VEXTRACTF128 $1, Y0, X1
	VADDPS X1, X0, X0
	VHADDPS X0, X0, X0
	VHADDPS X0, X0, X0

	MOVSS X0, ret+48(FP)
	RET
