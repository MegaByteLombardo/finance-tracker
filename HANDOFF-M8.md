# Handoff — M8 Credit & Debt (estado al 2026-10-25)

Documento de traspaso para el agente que continúe M8. **Es temporal**: no forma
parte del contrato de `docs/` y no debe commitearse salvo que el usuario lo pida.
Léelo entero antes de tocar código.

> **Actualización (2026-10-25, tarde): M8.2 está implementado y sin commitear**,
> pendiente de la validación funcional del usuario. Migración `00022`, TD-092,
> `internal/creditcard/{calendar,statement,obligations,reconcile,standing}.go`,
> `application.RecordCardPayment`, `savings.ApplyCardPaymentPlanTx` (D-12) y las
> pantallas `/cards/{id}/payments` y `/cards/{id}/statements/...`. Lo construido y
> las desviaciones del plan están en `docs/m8-contract.md` (sección M8.2) y en la
> cabecera de `docs/m8-plan.md`. Siguiente: M8.3 ∥ M8.4, cuando el usuario lo pida.

## 1. Cómo trabajar con este usuario

- Escribe al usuario **en español**. El código, los comentarios, los commits y los
  documentos de `docs/` van **en inglés**, salvo `docs/m8-plan.md`, que está en
  español.
- **Un submilestone por vez.** Cada uno se cierra con las puertas de calidad en
  verde, los docs actualizados en el mismo cambio y la **validación funcional del
  usuario**. Al terminar uno, detente, resume y espera antes de seguir.
- **No hagas commit sin pedirlo.** El usuario commitea por su cuenta y, a veces,
  retoca plantillas y handlers después. Toma el estado en disco como la verdad y
  relee los archivos antes de editarlos.
- Explica las decisiones con ejemplos numéricos sencillos. El usuario no es
  contador y lo agradece.
- `CLAUDE.md` y `docs/` son contrato. Léelos antes de diseñar:
  - `CLAUDE.md`, sección "M8";
  - `docs/m8-contract.md`: lo construido;
  - `docs/m8-plan.md`: el plan completo, rev. 2;
  - `docs/technical-decisions.md`, de TD-088 en adelante.

## 2. Estado

- Rama `feature/credito-m8`. M8.1 está **implementado, commiteado y validado** por
  el usuario: creó su tarjeta Plaza Black real (VES, corte el día 25), cargó sus
  compras del ciclo 26/09–25/10 con fecha de operación y las tasas BCV de esas
  fechas.
- Puertas de calidad de M8.1:
  - `go vet` limpio y `gofmt` limpio;
  - `go test ./... -count=1` en verde;
  - `-race` en verde en el contenedor Linux.
- **Cambios sin commitear** (solo docs, son del último turno):
  - `docs/m8-plan.md`: D-11 y D-12 resueltas;
  - `docs/m8-contract.md`: nota sobre D-12;
  - `docs/milestones.md`: `-race` marcado en verde.

  Inclúyelos en el primer commit de M8.2 o díselo al usuario.
- El **estado de cuenta real de Plaza aún no se ha emitido** (puede tardar hasta 5
  días hábiles desde el corte del 25/10). M8.2 se construye y prueba con datos
  sintéticos. Cuando llegue, el usuario lo registrará y se validará contra la
  conciliación.

### Decisiones tomadas por el usuario

| ID | Resolución |
| --- | --- |
| D-11 | `COSTS_FIRST` como valor por defecto **provisional**, configurable por tarjeta (`CHRONOLOGICAL`). Para Plaza es `UNKNOWN`. Solo afecta a M8.3. |
| D-12 | **Aprobada la alternativa**: el formulario de pago desde una cuenta USD ofrece consumir reservas M7 de esa cuenta en la misma transacción, vinculadas al pago. **Entra en M8.2.** Sin plan, el pago se registra y M7 reporta `OVERALLOCATED`. |
| §11 | Se adoptó `foreign_key_check` en `backup.Validate` (ya hecho en M8.1). |

### Numeración (cuidado)

- **Migraciones:** la última es `00021`. M8.2 empieza en **`00022`**.
- **TDs:** existen TD-088 a TD-091, TD-094 y TD-095. **TD-096 ya está tomada**: es
  el ticker tape, trabajo del usuario fuera de M8.
  - TD-092 (asignación derivada) es de M8.2 y TD-093 (efecto cambiario) de M8.3,
    tal como reserva el plan.
  - La que el plan llama "TD-096" (hechos de calendario y simulación, M8.5) debe
    renumerarse a la **siguiente libre**: compruébalo con
    `grep "^## TD-" docs/technical-decisions.md`.

## 3. Qué construyó M8.1: dónde está cada cosa

- **Migraciones:**
  - `00019`: rebuild de 12 pasos de `accounts` y `transactions`, NO TRANSACTION,
    con guardas en tabla TEMP; forward-only.
  - `00020`: `credit_cards`.
  - `00021`: `credit_charges`, `transaction_refunds` y `operation_requests`.
  - `operation_requests` tiene `account_id` además de `transaction_id`: es una
    desviación documentada en el plan.
- **`internal/account`:** `CreditCard`, `Nature()` (`Asset`/`Liability`),
  `AssetTypes()`, `RequireAsset`, `ErrNatureChange`. `UpdateMetadata` corre ahora
  en una transacción.
- **`internal/ledger`:**
  - tipo `Refund`;
  - `refund.go` (`RecordRefundTx`, `UpdateRefundTx`, `checkRefundsOf`,
    `RefundedMinor`);
  - `request.go` (`ValidateRequestKey`, `ReplayedRequestTx`, `RecordRequestTx`);
  - `IncomeItem.AccountType`, `TransactionView.RefundOf`;
  - `ListViewsForAccount` / `Service.ListAccountTransactions`;
  - `ListFlows` reporta los `REFUND` como `EXPENSE`.
- **`internal/creditcard`:**
  - `Card`, `TermsInput`, `Position`;
  - `Service.Get`, `List` y `UpdateTerms`;
  - `RequireCardTx`, `CreateTx`, `MarkChargeTx`, `UpdateChargeTx`, `IsChargeTx`,
    `ChargesOfAccount`;
  - `SetClock` en `export_test.go`.
- **`internal/application/card.go`:**
  - `OpenCreditCard`, `RecordCardPurchase`, `RecordCardCharge`,
    `UpdateCardCharge`, `RecordRefund`, `UpdateRefund`;
  - los helpers `recordOnce` y `replay` implementan la idempotencia con
    `operation_requests`;
  - `UpdateTransaction` rechaza cargos (`ErrChargeEditedAsCharge`);
  - `OpenAccount` rechaza `CREDIT_CARD`.
- **`internal/savings`:** el primer paso de `rules.decide` exige una cuenta ASSET;
  `fundTx`, `checkFundAccount` (`ErrLiabilityAccount`) y `AccountImpact` solo
  aceptan activos (TD-094).
- **`internal/valuation`:** `AssetsMinor`/`LiabilitiesMinor`,
  `AccountComponent.Nature`, y el `Share` de cada cuenta sobre los activos.
- **`internal/database`:** `CheckForeignKeys`. `migrations.Up` lo exige tras
  migrar.
- **Web:**
  - `internal/web/card_types.go` (`CardService`, `CreditCardView`, `CardMovement`,
    errores);
  - `cards.go`, `refunds.go`, `fake_cards.go`, `cards_test.go`;
  - `internal/web/adapter/cards.go`: `toCardMovement` infiere la etiqueta de
    forma estructural, y `TranslateCardError`;
  - plantillas en `web/templates/cards/*`, `web/templates/refunds/form.html` y
    `components/card_terms_fields.html`, registrada en `RenderPage`;
  - el handler genérico de edición redirige `REFUND` y los cargos a sus formularios;
  - el formulario `/transfers/new?to_account_id=` preselecciona la tarjeta como
    destino;
  - el dashboard muestra los grupos Activos/Pasivos.
- **Tests de referencia:**
  - `internal/migrations/m8_upgrade_internal_test.go`: inyección de fallos por
    guarda, re-ejecución y upgrade 18→21;
  - `internal/application/card_test.go`;
  - `internal/savings/liability_test.go`;
  - `internal/valuation/liability_test.go`;
  - `internal/ledger/refund_test.go`.

## 4. Siguiente trabajo: M8.2 — Statements & Payments

Fuente: `docs/m8-plan.md`, especialmente estas secciones:

| Sección | Contenido |
| --- | --- |
| R2.2–R2.5 | Modelo H, capa O, escenarios E1–E7, devoluciones |
| R7 | Fechas y aserciones de pertenencia |
| R8 | Conciliación |
| §4.2 | Tablas de 00022 |
| §5.4 | Calendario |
| §8 | Rutas |
| §12 / M8.2 | Aceptación |

### Alcance

1. **Migración `00022`:**
   - `credit_statements`: `id`, `card_account_id` → `credit_cards`,
     `period_start ≤ closing_date`, `due_date` NULL `> closing_date`,
     `closing_balance_minor` con la convención del ledger,
     `minimum_payment_minor` NULL `≥ 0`, y NULL para `previous_balance_minor`,
     `payments_minor`, `debits_minor` y `past_due_minor`;
     `UNIQUE(card_account_id, closing_date)`. El no solapamiento se valida en Go.
   - `credit_statement_assignments`: `transaction_id` PK y `statement_id`
     indexado. En Go se valida que el movimiento sea de esa tarjeta y que el corte
     sea `≥ effective_date`.
   - `ALTER TABLE credit_cards ADD COLUMN refund_policy TEXT NOT NULL DEFAULT 'NEXT_STATEMENT' CHECK (...)`.
   - Todo `STRICT` y reversible. Actualiza `schema.go` (`tableIntroducedAt`),
     `schema_test.go` y las constantes `latestSchemaVersion` y `expectedVersion`.
2. **Calendario** (`internal/creditcard`):
   - `closing(m) = min(statement_day, último día de m)`;
   - un movimiento pertenece al ciclo que cierra en `closing(mes(x))` si
     `día(x) ≤ closing`, y si no, al del mes siguiente;
   - un estado registrado prevalece en su rango y una aserción prevalece sobre la
     regla;
   - la pertenencia **se deriva y nunca se guarda**.
3. **Capa O: obligaciones** (R2.3), en una función pura y determinista con traza:
   - deuda total, facturado pendiente, mínimo pendiente, `V(t)` como **máximo**
     (no suma), mínimo incumplido, anticipado y saldo a favor;
   - el estado del ciclo (`OPEN`/`BILLED`/`PARTIALLY_PAID`/`MINIMUM_MET`/`PAID`/`OVERDUE`/`DUE_UNKNOWN`)
     se deriva;
   - la asignación de cada pago es la partición de `[p, p+x)` en tramos acotados
     (I5);
   - las devoluciones nunca cuentan como pago ni para el mínimo; con
     `REDUCES_BILLED` sí reducen el facturado;
   - un vencimiento desconocido **nunca** da `OVERDUE`. Nada de esto se persiste
     (TD-092).
4. **Pagos:** `application.RecordCardPayment` crea un TRANSFER si la moneda es la
   misma y un FX_EXCHANGE si no. Lleva `RequestKey` y **el plan de ahorro de
   D-12**:
   - extender `internal/savings`, que hoy solo admite `SPEND` vinculado a un
     `EXPENSE` (`ApplyExpensePlanTx`, `issues.go` verifica que un `SPEND` siga
     apuntando a un gasto válido);
   - definir cómo se vincula el consumo de reservas a un pago cuyo origen es un
     activo USD y cuyo destino es un pasivo, y ajustar las incidencias sin romper
     la suite de M7;
   - la pantalla `/cards/{id}/payments` muestra los montos sugeridos (vencido,
     mínimo, facturado), nunca envía sola, y muestra `AccountImpact` del origen
     USD.
5. **Estados y conciliación:**
   - `/cards/{id}/statements`;
   - `/cards/{id}/statements/{sid}/reconcile` muestra `Δ_j = B_j − L_j`, sus
     componentes si el banco reportó cifras, y los candidatos cerca del corte;
   - `AssertStatementMembership` registra las aserciones;
   - cambiar el `closing_date` se rechaza si deja una aserción posterior al nuevo
     corte;
   - la conciliación informa y **nunca corrige sola**.
6. **Docs en el mismo cambio:**
   - TD-092;
   - `m8-contract.md`, sección M8.2;
   - `data-model.md`, `milestones.md`, `CLAUDE.md` (sección M8) y la cabecera de
     `m8-plan.md`.

### Aceptación (§12 y R12)

- E1–E7 con los números de R2.4, exactos;
- I5 con secuencias generadas;
- umbrales acotados con advertencia;
- `V` como máximo;
- mínimo desconocido y ningún estado vigente;
- corte editado frente a una aserción posterior, rechazado;
- aserción a un corte anterior a la fecha, rechazada;
- pago desde USD (con y sin plan de ahorro) y desde VES;
- calendario: día 25, cambio de año, 31 en febrero y bisiesto;
- conciliación con aserciones;
- la suite de M7 sin cambios.

## 5. Trampas del entorno (Windows + Git Bash)

- **`core.autocrlf=true`.** Los archivos del working tree tienen CRLF y `gofmt -l`
  los marca todos. Para verificar el formato:
  ```bash
  for f in $(git ls-files -m -o --exclude-standard '*.go'); do out=$(tr -d '\r' < "$f" | gofmt -d 2>&1); [ -n "$out" ] && echo "NOT FORMATTED: $f"; done
  ```
  **No corras `gofmt -w` sobre directorios enteros**: convierte a LF archivos que
  no tocaste y ensucia el diff. Formatea solo los archivos que creaste.
- **Heredocs** con `<<'EOF'` en el Bash tool a veces fallan con "unexpected EOF"
  cuando el contenido es largo y tiene apóstrofos. Usa el tool Write (a un archivo
  del scratchpad si hace falta) y luego `cat >>` o un script de Python.
- **Git Bash reescribe los argumentos que empiezan con `/`**, como `return_to=/cards/1`
  en `curl -d` o los volúmenes de Docker. Antepón `MSYS_NO_PATHCONV=1`.
- **`-race`** se corre en el contenedor (unos 10 minutos):
  ```bash
  MSYS_NO_PATHCONV=1 docker run --rm -v "$(pwd -W):/src" -w /src golang:1.27-trixie go test -race -count=1 ./...
  ```
- **Smoke test del binario:**
  ```bash
  go build -o <scratch>/ft.exe ./cmd/web
  HTTP_ADDR=127.0.0.1:18089 DB_PATH=./data/finance.db BACKUP_DIR=./backups FX_BCV_ENABLED=false ./ft.exe
  ```
  Lánzalo en segundo plano desde el directorio del scratchpad y detenlo con
  `taskkill //F //IM ft.exe`.
- **El pool tiene una sola conexión.** Dentro de una transacción no llames nunca a
  un servicio que use el `*sql.DB`: se bloquea para siempre. Lee con
  `NewRepository(exec)`.
- **TD-045 en todas las capas**, también en la web: cada guarda nombra lo que
  acepta. Los switch del handler genérico de transacciones ya redirigen `REFUND` y
  los cargos; añade los casos nuevos de la misma forma.
