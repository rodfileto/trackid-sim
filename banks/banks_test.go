package banks

import "testing"

func TestSplitNumber(t *testing.T) {
	for _, c := range []struct{ endereco, numero, street, number string }{
		{"R.SEN.DANTAS", "105", "Rua Senador DANTAS", "105"},
		{"R SENADOR DANTAS", "", "Rua SENADOR DANTAS", ""},
		{"RUA GOV. CELSO RAMOS, NUM 381", "", "RUA Governador CELSO RAMOS", "381"},
		{"AV. N. SRA. DE COPACABANA, 540", "", "Avenida Nossa Senhora DE COPACABANA", "540"},
		{"EST INTENDENTE MAGALHAES", "", "Estrada INTENDENTE MAGALHAES", ""},
		{"AVENIDA RIO BRANCO", "33", "AVENIDA RIO BRANCO", "33"},
		{"PRAIA DE BOTAFOGO,384-A", "", "Praia DE BOTAFOGO", "384"},
		{"AV AVENIDA REPUBLICA DO CHILE", "230", "Avenida REPUBLICA DO CHILE", "230"},
		{"R.VISC.DE INHAUMA,74/76", "", "Rua Visconde DE INHAUMA", "74"},
		{"RUA S JOSE", "", "RUA S JOSE", ""}, // a bare one-letter word mid-street stays
	} {
		street, number := splitNumber(Raw{Endereco: c.endereco, Numero: c.numero})
		if street != c.street || number != c.number {
			t.Errorf("%q %q: got %q %q, want %q %q", c.endereco, c.numero, street, number, c.street, c.number)
		}
	}
}

func TestIsBank(t *testing.T) {
	for seg, want := range map[string]bool{
		"Banco Múltiplo": true, "Caixa Econômica Federal": true, "Banco Comercial": true,
		"Sociedade Corretora de TVM": false, "Sociedade de Crédito Direto": false,
	} {
		if IsBank(seg) != want {
			t.Errorf("IsBank(%q) = %v", seg, !want)
		}
	}
}
