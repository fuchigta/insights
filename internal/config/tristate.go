package config

// Tristate は "true" / "false" / "auto" の三値を表す。
// "auto" は「コマンドなどが見つかれば使う」という best-effort な既定を表す。
type Tristate string

const (
	TristateTrue  Tristate = "true"
	TristateFalse Tristate = "false"
	TristateAuto  Tristate = "auto"
)

// Valid は t が既知の三値のいずれかであるかを返す。
func (t Tristate) Valid() bool {
	switch t {
	case TristateTrue, TristateFalse, TristateAuto:
		return true
	default:
		return false
	}
}

// Enabled は found（対応するコマンド等が見つかったか）を踏まえて有効/無効を判定する。
//   - "true"  なら常に有効
//   - "false" なら常に無効
//   - "auto"（またはその他の不正値）なら found の値をそのまま使う
func (t Tristate) Enabled(found bool) bool {
	switch t {
	case TristateTrue:
		return true
	case TristateFalse:
		return false
	default:
		return found
	}
}
