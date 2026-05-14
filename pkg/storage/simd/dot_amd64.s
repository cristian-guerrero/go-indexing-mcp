//go:build amd64

#include "textflag.h"

// func dotAVX2(a, b []float64) float64
//
// Computes dot product using AVX2 + FMA.
// Unrolls 4x (16 float64s per iteration) for ILP.
TEXT ·dotAVX2(SB), NOSPLIT, $0-56
	MOVQ a_base+0(FP), AX
	MOVQ b_base+24(FP), BX
	MOVQ a_len+8(FP), CX

	VXORPD Y0, Y0, Y0
	VXORPD Y1, Y1, Y1
	VXORPD Y2, Y2, Y2
	VXORPD Y3, Y3, Y3

	CMPQ CX, $16
	JL   block4

	MOVQ CX, R8
	SHRQ $4, CX

loop16:
	VMOVUPD (AX), Y4
	VMOVUPD (BX), Y5
	VFMADD231PD Y4, Y5, Y0

	VMOVUPD 32(AX), Y6
	VMOVUPD 32(BX), Y7
	VFMADD231PD Y6, Y7, Y1

	VMOVUPD 64(AX), Y8
	VMOVUPD 64(BX), Y9
	VFMADD231PD Y8, Y9, Y2

	VMOVUPD 96(AX), Y10
	VMOVUPD 96(BX), Y11
	VFMADD231PD Y10, Y11, Y3

	ADDQ $128, AX
	ADDQ $128, BX
	DECQ CX
	JNZ loop16

	VADDPD Y1, Y0, Y0
	VADDPD Y3, Y2, Y2
	VADDPD Y2, Y0, Y0

	MOVQ R8, CX
	ANDQ $15, CX

block4:
	MOVQ CX, DX
	SHRQ $2, DX
	JZ   scalar

loop4:
	VMOVUPD (AX), Y4
	VMOVUPD (BX), Y5
	VFMADD231PD Y4, Y5, Y0

	ADDQ $32, AX
	ADDQ $32, BX
	DECQ DX
	JNZ loop4

scalar:
	ANDQ $3, CX
	JZ   done

scalar_loop:
	MOVSD (AX), X4
	MOVSD (BX), X5
	MULSD X5, X4
	ADDSD X4, X0

	ADDQ $8, AX
	ADDQ $8, BX
	DECQ CX
	JNZ scalar_loop

done:
	VHADDPD Y0, Y0, Y0
	VEXTRACTF128 $1, Y0, X1
	VADDSD X1, X0, X0

	MOVQ X0, ret+48(FP)
	RET
