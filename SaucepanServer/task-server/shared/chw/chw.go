// Package chw computes 2 m-equivalent hardware weight for integration budgets.
package chw

const (
	// DRefMM is the reference aperture diameter (2 m).
	DRefMM = 2000.0
	// QERef is the reference quantum efficiency.
	QERef = 1.0
)

// CHw returns the hardware coefficient: (D/D_ref)^2 * (QE/QE_ref).
// Unknown or non-positive QE is treated as 1.0. Non-positive aperture returns 0.
func CHw(apertureMM, qe float64) float64 {
	if apertureMM <= 0 {
		return 0
	}
	if qe <= 0 {
		qe = QERef
	}
	ratio := apertureMM / DRefMM
	return ratio * ratio * (qe / QERef)
}
