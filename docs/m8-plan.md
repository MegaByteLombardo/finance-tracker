# M8 — Credit & Debt Management: auditoría y plan técnico (rev. 2)

**Estado:** la arquitectura está APROBADA CONCEPTUALMENTE. La revisión 2 incorpora
las correcciones del review. **M8.1 está implementado** (migraciones 00019–00021,
TD-088 a TD-091, TD-094 y TD-095) y pendiente de validación funcional; lo
construido está en [m8-contract.md](m8-contract.md). Se adoptaron las propuestas
del §11 que tocan a M8.1: `foreign_key_check` en `backup.Validate`. D-11 se
aprobó como valor por defecto provisional y D-12 se resolvió por la alternativa
(ver R1). M8.2–M8.5 no tienen código.

Desviación respecto de §4.2: `operation_requests` tiene, además de
`transaction_id`, un `account_id` (ambos opcionales, al menos uno presente),
porque abrir una tarjeta sin deuda previa no produce ninguna transacción y
también debe ser idempotente.

**Leyenda.**
- `CONFIRMED`: lo fija el requerimiento o el review, o se verificó en el código o
  la documentación oficial.
- `PROPOSED`: necesita aprobación.
- `UNKNOWN`: dato no disponible.

**Base.** `feature/credito-m8` se avanzó (*fast-forward*) a
`feat/plan-de-ahorro` @ `a516f47` (D-1). Todas las referencias `archivo:línea`
corresponden a esa base, que ya incluye M7. M7 **no** se fusiona a `main` hasta
que termine su validación.

Al aprobarse, este documento se convierte en `docs/m8-contract.md` (en inglés) y
en las TD-088 y siguientes.

---

## Architecture Review — Resolved Findings

### R1. Decisiones finales

| ID | Decisión | Estado | Resultado |
| --- | --- | --- | --- |
| D-1 | M8 sobre M7 | APPROVED, aplicado | Rama en `a516f47`. Migraciones desde `00019`, TD desde **TD-088**. |
| D-2 | Ampliación de los CHECK | APPROVED WITH CONDITIONS | Procedimiento SQLite de 12 pasos, con todas las condiciones verificadas en R6. |
| D-3 | Métricas cambiarias | APPROVED | Se recalculan en cada lectura. Cada importe expone sus *legs* (fecha, fuente y si son manuales). Nunca se guardan. |
| D-4 | Beneficio económico confirmado | APPROVED | Solo existe con **evidencia estructural** de financiamiento en USD (R4.4). |
| D-5 | Asignación de pagos | **RESUELTA AQUÍ** (R2) | Asignación **derivada y determinista** en dos capas: obligaciones y componentes. Solo se persisten **hechos** del banco y **aserciones** del usuario, no resultados. |
| D-6 | ADJUSTMENT | APPROVED | Pospuesto. |
| D-7 | Devoluciones | APPROVED WITH CONDITIONS → **resuelta** (R2.5) | Nunca cuentan como pago ni reducen el mínimo. Su efecto sobre el facturado depende de una política explícita por tarjeta (`refund_policy`). |
| D-8 | Fecha de operación o de contabilización | **RESUELTA AQUÍ** (R7) | `effective_date` = fecha de operación, siempre. La pertenencia a un estado de cuenta se deriva y se corrige con una aserción persistida (`credit_statement_assignments`, M8.2). M8.1 no se bloquea. |
| D-9 | `network` | APPROVED | Omitido. |
| D-10 | `CLAUDE.md` | APPROVED | Una TD por cada prohibición que se reemplaza (R9). |
| **D-11** | Orden de imputación dentro de un ciclo (capa de componentes) | **APPROVED como valor por defecto provisional** (2026-10-25) | `COSTS_FIRST` por defecto, configurable por tarjeta (`CHRONOLOGICAL`). Para Plaza sigue `UNKNOWN`: el estado de cuenta solo muestra "mínimo" y "total", y la regla de imputación está en la cláusula de imputación de pagos del contrato. Solo afecta a la partición realizado/potencial de M8.3, nunca a la deuda ni al total (I6); sin intereses ni comisiones, las dos políticas coinciden. |
| **D-12** | Plan de ahorro M7 al pagar una TDC con USD reservado | **APPROVED: alternativa** (2026-10-25) | El formulario de pago desde una cuenta USD ofrece, en la misma operación, consumir reservas de esa cuenta (como `SPEND` de un gasto, TD-086), vinculadas al pago y en la misma transacción. Sin plan, el pago se registra y M7 reporta `OVERALLOCATED` como hoy. Entra en **M8.2** con la pantalla de pago. |

### R2. Modelo de asignación de pagos

#### R2.1 Tres responsabilidades distintas

| Responsabilidad | Pregunta | Dónde vive |
| --- | --- | --- |
| **Liquidación contable** | ¿Cuánto se debe? | Ledger: saldo de la cuenta TDC (sin cambios) |
| **Asignación a obligaciones** | ¿Qué exigencia del banco satisface este pago (vencido, mínimo, facturado o anticipado)? | `creditcard`, capa O (R2.3) |
| **Atribución a componentes** | ¿Qué compras, intereses o comisiones retira este crédito? | `creditanalytics`, capa C (R2.6), para el motor cambiario |

La liquidación y la asignación son responsabilidades distintas: el review tiene
razón. Lo que sigue decide *cómo se materializa* la asignación, no si existe. La
asignación es un objeto de dominio de primera clase
(`Allocation{CreditTxID, Target, Component, AmountMinor, Trace}`), se lista en la
UI y se prueba.

#### R2.2 ¿Asignaciones persistidas o derivadas?

| Criterio | P: `payment_allocations` persistida al registrar | D: derivada en cada lectura | **H: derivada + hechos y aserciones persistidos** (elegida) |
| --- | --- | --- | --- |
| 1. Exactitud | Exacta al escribirla. Queda **obsoleta** cuando cambia un hecho anterior: una compra retroactiva, la edición de un estado de cuenta o un pago con fecha anterior. | Siempre exacta respecto de los hechos vigentes. | Igual que D, y además incorpora lo que el banco hizo distinto de la regla. |
| 2. Auditabilidad | Filas fijas, pero sin explicar por qué quedaron así. | Una traza determinista y reproducible por cada asignación. | Traza más aserciones con fecha de creación, más las cifras que reportó el banco. |
| 3. Compatibilidad con el ledger | Es una segunda fuente de verdad de algo derivable: el mismo modo de falla que TD-052 prohíbe (`current_net_worth`). | Ninguna duplicación. | Ninguna duplicación. Lo persistido son entradas, no resultados. |
| 4. Reconstrucción histórica | Exige reescribir en cascada (la asignación de N depende de N−1). | "A una fecha" = cortar las entradas en esa fecha. | Igual que D. |
| 5. Conciliación con el banco | El usuario corrige filas calculadas a mano y la siguiente edición las pisa. | No puede representar una imputación distinta de la regla. | Lo que el banco hizo distinto se declara como **hecho** (R7) y se compara con lo que reportó (R2.7). |
| 6. Complejidad | Alta: invalidación, recálculo, concurrencia. | Media: una función pura. | Media, con 1 tabla pequeña más algunos campos opcionales. |

**Decisión (`PROPOSED`): H.** Los únicos datos que se persisten son:
- los estados de cuenta (hechos del banco, con cifras opcionales que reportó);
- `credit_statement_assignments` (en qué estado de cuenta incluyó el banco un
  movimiento, cuando difiere de la regla de fechas);
- `transaction_refunds` (qué compra devuelve una devolución);
- la política de la tarjeta (`refund_policy`, `imputation_order`).

Nada calculado se guarda.

Si algún día un banco reporta una imputación línea a línea que contradiga las
reglas, se añadiría `credit_allocation_assertions`: otra tabla de **aserciones**,
nunca de caché. Hoy no existe ese insumo.

#### R2.3 Capa O: obligaciones (estado de cuenta)

**Notación.** Por tarjeta, en unidades menores y con lo adeudado en positivo
(ledger con signo invertido).

- Los ciclos `j = 1..n` se ordenan por fecha de corte `C_j`. Cada uno es
  **registrado** (existe un `credit_statement`) o **calculado** (calendario,
  §5.4).
- Lo adeudado al corte `j`:
  - `B_j` es el saldo que reportó el banco, si el ciclo está registrado;
  - si no, `L_j` = neto del ledger de los movimientos cuya pertenencia es `≤ j`,
    rotulado "calculado".
- `O_j = max(0, B_j)` es la obligación facturada. Si `B_j < 0` es saldo a favor y
  no hay obligación.
- `M_j` es el mínimo (solo si está registrado; si no, "desconocido"). `D_j` es el
  vencimiento (registrado, estimado por la regla de la tarjeta o "desconocido").
- La **pertenencia** de un movimiento a un ciclo es la aserción
  (`credit_statement_assignments`), si existe; si no, la regla de fechas.
- `P_j(t)` = Σ de los **pagos** (TRANSFER o FX_EXCHANGE hacia la TDC) con
  pertenencia `> j` y fecha `≤ t`. Son los pagos posteriores al corte `j`. Un
  pago con pertenencia `≤ j` ya está *dentro* de `B_j`.

**Propiedad clave: las obligaciones están anidadas.** En crédito rotativo, `B_{j+1}`
incluye lo no pagado de `B_j`, y un pago posterior a `C_{j+1}` también es posterior
a `C_j`. Por eso un pago reduce a la vez **todas** las obligaciones abiertas, y
nada se suma dos veces. Que Plaza anide también el mínimo (que `M_{j+1}` incluya
el vencido de `j`) es `UNKNOWN`. Se verifica con el campo `past_due_minor` del
primer estado de cuenta que tenga atraso; ver R8.

Cifras en la fecha `t`. `k` es el último ciclo con `C_k < t`, el estado de cuenta
vigente.

| Cifra | Fórmula |
| --- | --- |
| Deuda total | `max(0, −saldo_ledger(t))`. Si es negativo, se muestra como saldo a favor. |
| Facturado pendiente | `max(0, O_k − P_k(t) − R_k(t)·[refund_policy = REDUCES_BILLED])` |
| Mínimo pendiente | `max(0, M_k − P_k(t))`. Las devoluciones **nunca** cuentan. |
| Vencido exigible | `V(t) = max_{j : D_j < t} max(0, M_j − P_j(t))`. Es el **máximo**, no la suma: un pago nuevo sube todos los `P_j` a la vez, así que el monto que regulariza todos los mínimos atrasados es el máximo de los déficits. |
| Mínimo incumplido (histórico) | `max(0, M_j − P_j(D_j))` para cada `j` con `D_j ≤ t`. Es un hecho fijo de ese ciclo. |
| Facturado no cancelado al vencimiento | `max(0, O_j − P_j(D_j))`. Es la base sobre la que el banco cobraría intereses; **no los calculamos**. |
| Consumos posteriores al corte | Débitos con pertenencia `> k` y fecha `≤ t`. |
| Anticipado | `max(0, P_k(t) − O_k)`. Reduce el próximo `B`. |
| Estado del ciclo `j` | `OPEN`, `BILLED`, `PARTIALLY_PAID`, `MINIMUM_MET`, `PAID`, `OVERDUE`, `DUE_UNKNOWN`. Se deriva y nunca se guarda. |

**Asignación de un pago** (la traza que muestra "Aplicación del pago"). Sea `p` el
acumulado de pagos posteriores a `C_k` antes de este y `x` su importe. Los
umbrales sobre la recta acumulada del ciclo vigente son:

```
[0, V)          → VENCIDO (indica qué estados j están atrasados)
[V, M_k)        → MÍNIMO
[M_k, O_k)      → RESTO FACTURADO
[O_k, O_k + U)  → ANTICIPADO al ciclo abierto (U = débitos no facturados)
[O_k + U, ∞)    → SALDO A FAVOR
```

- La asignación es la intersección de `[p, p + x)` con esos tramos. Es una
  **partición del importe del propio pago**: suma exactamente `x` y no se repite.
- Los umbrales se acotan para que el orden sea monótono
  (`V' = min(V, O_k)`, `M' = min(max(M_k, V'), O_k)`). Si se acotan, la vista
  muestra una advertencia de conciliación.
- Sin `M_k`, el tramo de mínimo desaparece. Sin estado de cuenta vigente, todo es
  anticipado o saldo a favor.

#### R2.4 Escenarios exigidos (corte el 25)

Datos comunes:
- Estado de **septiembre**: `C=2026-09-25`, `D=2026-10-15`, `B=10.000`, `M=1.000`.
- Estado de **octubre**: `C=2026-10-25`, `D=2026-11-14`, `B=18.500`, `M=2.500`
  (el banco reporta un vencido de 700).

| # | Escenario | Hechos | Resultado |
| --- | --- | --- | --- |
| E1 | **Dos estados pendientes a la vez y pago parcial** | Pago de 300 el 05/10, menor que el mínimo. Pago de 1.000 el 30/10. | Al 15/10, el mínimo de septiembre quedó incumplido por 700 y quedaron 9.700 sin cancelar. Al 28/10: `V = max(1.000−300) = 700`, mínimo de octubre pendiente 2.500 y facturado 18.500. El pago del 30/10 se reparte en **700 a VENCIDO (septiembre) + 300 a MÍNIMO (octubre)**. Después, `V = 0`, mínimo pendiente 1.500 y facturado 17.500. Un solo pago redujo dos obligaciones sin contarse dos veces. |
| E2 | **Pago anticipado** | Septiembre pagado completo (10.000) el 05/10; otro pago de 500 el 20/10. | El segundo pago va completo a ANTICIPADO. El `B` de octubre del ledger ya lo descuenta, y la conciliación con el `B` real lo confirma. |
| E3 | **Devolución posterior al corte** | Devolución de 2.000 el 02/10, vinculada a una compra de agosto. | La deuda total baja 2.000. Con `NEXT_STATEMENT` (por defecto), el facturado de septiembre **sigue en 10.000** y se informa "neto de devoluciones: 8.000"; el `B` de octubre del banco ya la descuenta. Con `REDUCES_BILLED`, el facturado es 8.000. El mínimo es 1.000 en ambos casos. |
| E4 | **Intereses y comisiones** | Interés de 300 y comisión por atraso de 100, fechados el 25/10. | Son débitos del ciclo de octubre, incluidos en `O_oct`. En la capa O son obligación como cualquier otra. Distinguirlos solo importa en la capa C. |
| E5 | **Saldo a favor** | `B_sep = −500`. | `O = 0`, `M = 0`. Todo pago es ANTICIPADO o SALDO A FAVOR. Una compra posterior consume primero el saldo a favor (capa C). |
| E6 | **Compra retroactiva** | Se registra hoy una compra del 20/08, ya facturada en septiembre. | La obligación de septiembre **no cambia**, porque `B` es un hecho del banco. La diferencia de conciliación `B − L` de septiembre pasa de 3.000 a 0. Si septiembre no estuviera registrado, su `L` calculado subiría 3.000 y todo se recalcularía. Nada guardado queda obsoleto. |
| E7 | **Modificación de un estado de cuenta** | El mínimo de octubre se corrige de 2.500 a 2.600. | Todas las cifras y la traza del pago del 30/10 se recalculan (700 + 300 sigue valiendo: `[0,700)`, `[700,2.600)`). Cambiar `closing_date` se rechaza si deja una aserción con fecha posterior al nuevo corte. |

#### R2.5 Devoluciones (D-7)

- **No son un pago.** Nunca entran en `P_j` y nunca reducen el mínimo ni el
  vencido.
- **Siempre** reducen la deuda total y el ciclo abierto (el próximo `B`).
- Su efecto sobre el facturado vigente es una **política de la tarjeta**:
  `refund_policy IN ('NEXT_STATEMENT','REDUCES_BILLED')`, con `NEXT_STATEMENT` por
  defecto. Para Plaza es `UNKNOWN`. El siguiente estado de cuenta real muestra cuál
  aplica: la conciliación R2.7 lo hace visible.
- En la capa C, una devolución vinculada retira **su** compra. Si no está
  vinculada, retira principal siguiendo el orden de R2.6.

#### R2.6 Capa C: componentes, para el motor cambiario

La clasificación es **estructural** (TD-044): tipo, signo y fila en
`credit_charges`. Nunca por categoría ni descripción.

| Componente | Origen en el ledger | Naturaleza |
| --- | --- | --- |
| `PURCHASE` | EXPENSE en la TDC sin `credit_charges` | Principal de compras |
| `CASH_ADVANCE` | TRANSFER que **sale** de la TDC | Otro principal |
| `OPENING_DEBT` | OPENING_BALANCE negativo | Otro principal |
| `INTEREST`, `FEE`, `PENALTY` | EXPENSE + `credit_charges.kind` | Costos financieros |
| `PAYMENT` | TRANSFER o FX_EXCHANGE que **entra** a la TDC | Crédito |
| `REFUND` | REFUND en la TDC (vinculado o no) | Crédito |
| `OTHER_CREDIT` | INCOME en la TDC (por ejemplo, cashback), OPENING_BALANCE positivo | Crédito |

**Orden de retiro (determinista):**
1. Una devolución vinculada retira su compra (hasta lo que quede de ella).
2. Cualquier otro crédito retira, en orden: los componentes **facturados** del
   ciclo más antiguo con saldo, y dentro del ciclo según `imputation_order`:
   - `COSTS_FIRST` (por defecto, D-11): PENALTY → INTEREST → FEE → principal por
     `(effective_date, id)`;
   - `CHRONOLOGICAL`: todo por `(effective_date, id)`.
3. Después, los componentes **no facturados**, por `(effective_date, id)`.
4. Lo que sobre forma un **saldo a favor**: un componente acreedor que retirarán
   los débitos posteriores.

¿Por qué `COSTS_FIRST`? Es la práctica habitual en los contratos de crédito
rotativo, y además es **conservador**: atribuye *menos* beneficio realizado al
principal en los primeros pagos. El total no cambia, por la invariante I6.

### R3. Fórmulas cambiarias revisadas

Notación:
- Unidad de componente: importe `a` (moneda de la tarjeta), originada en la fecha
  `o` y retirada en `r` por un crédito de tipo `k`.
- `R(d)` es el factor moneda de la tarjeta → base resuelto por `fx.Book` para la
  fecha `d`.
- `E = a·R(o)` es el valor original. `S = a·R(r)` es el valor de referencia al
  retiro. `S_T = a·R(T)` es el valor hoy.
- `X` son los USD efectivamente entregados (solo para pagos con evidencia de USD,
  R4.4), prorrateados con la tasa de **ejecución** del FX_EXCHANGE.

Todo se calcula en `big.Rat` exacto y se redondea **una vez**, half-even, por
cifra presentada.

| Indicador | Unidades que incluye | Fórmula |
| --- | --- | --- |
| **A. Efecto de valoración potencial** | Principal pendiente / costos pendientes, en líneas separadas | `Σ (E − S_T)` |
| **B. Efecto de valoración realizado** | Principal retirado por PAYMENT u OTHER_CREDIT / costos retirados, en líneas separadas | `Σ (E − S)` |
| **C. Beneficio económico confirmado** | Solo unidades retiradas por pagos **con evidencia de USD** | Principal: `Σ (E − X)`. Costos: `− Σ X`. |
| **Costos financieros a su valor original** | Todos los cargos | `K = Σ_costos E`. Es la misma cifra que M6 ya muestra como gasto (TD-068). |
| **D. Beneficio neto** | Igual que A y B | `D = B_principal + B_costos − K_retirados` (realizado) y `A_principal + A_costos − K_pendientes` (estimado). **Siempre en dos líneas; nunca se suman entre sí sin rótulo.** |
| Efecto en devoluciones | Principal retirado por REFUND | `Σ (E − S)`. **Excluido** de B, C y D (R3.3). |
| Efecto sobre el saldo a favor | Componentes acreedores | `Σ (S − E)`. Línea aparte, excluida de D. |

#### R3.1 Prueba de que los costos no se cuentan dos veces

Para una unidad de costo, su aporte a D es `(E − S) − E = −S`. **Cada costo entra
en D exactamente una vez, a su valor en USD al momento de pagarlo**, que es su
carga real.

Para una unidad de principal, el aporte es `E − S`. Entonces:

```
D = Σ_principal (E − S)  −  Σ_costos S
```

Las dos formas erróneas se descartan explícitamente con pruebas:
- **(i)** Restar `K` sin sumar el efecto cambiario sobre los costos sobrestima el
  costo en `E − S`.
- **(ii)** Sumar el efecto cambiario sobre los costos y restar su valor al pago
  (`S`) cuenta dos veces la depreciación.

#### R3.2 Ejemplos numéricos (exactos)

**X1. Requerimiento §6.1.** Compra de 10.000 a 200, pagada desde VES a 250.
- `E = 50`, `S = 40`.
- **B = 10,00** (2.000 bp = 20 %).
- C: no atribuible (pago con VES).
- D = 10,00.

**X2. Intereses sin doble conteo** (`COSTS_FIRST`).
- Compra de 10.000 a 200 (`E = 50`). Interés de 500 fechado en el corte a 220
  (`E = 25/11 = 2,2727…`). Pago de 10.500 a 250.
- El interés se retira primero (`S = 2`); el principal, después (`S = 40`).

| Línea | Valor exacto | Presentado |
| --- | --- | --- |
| B principal | 50 − 40 | 10,00 |
| B costos (efecto cambiario sobre el interés) | 25/11 − 2 = 3/11 | 0,27 |
| K (interés a su valor original) | 25/11 | 2,27 |
| **D realizado** | 10 + 3/11 − 25/11 = **8** | **8,00** |
| Comprobación con `Σ(E−S)_principal − Σ S_costos` | 10 − 2 | 8,00 ✓ |
| Forma errónea (i) | 10 − 25/11 | 7,73 ✗ |
| Forma errónea (ii) | 10 + 3/11 − 2 | 8,27 ✗ |

**Coherencia con el patrimonio.**
- La línea de deuda pasó de −(50 + 25/11) = −52,27 USD (contabilizado) a −42 USD
  justo antes del pago.
- Esa variación de **+10,27 = 10 + 3/11 = A + B sobre todas las unidades** **ya
  está** en el patrimonio, porque la valoración convierte el saldo con la tasa del
  día.
- `creditanalytics` solo la *explica*. No la vuelve a sumar (invariante I1).

**X3. Pago parcial; el orden no altera el total.**
- Mismos cargos que X2, pero el pago es de 5.000 a 250 y hoy la tasa es 260.

| Política | Retiros | D realizado | D estimado | **D total** |
| --- | --- | --- | --- | --- |
| `COSTS_FIRST` | Interés 500 (`S = 2`) y principal 4.500 (`S = 18`, `E = 22,5`) | 4,5 + 3/11 − 25/11 = **2,50** | Principal 5.500: 27,5 − 275/13 = **6,35** | **115/13 = 8,85** |
| `CHRONOLOGICAL` | Principal 5.000 (`S = 20`); el interés queda pendiente | **5,00** | (25 − 250/13) + (25/11 − 25/13) − 25/11 = 50/13 = **3,85** | **115/13 = 8,85** ✓ |

La política solo cambia **qué parte es realizada y cuál potencial**. El total es
`D = Σ_principal E − Σ_todas las unidades S` (con `S` al retiro o a hoy), así que
no depende del orden: 50 − 18 − 275/13 − 2 = 50 − 20 − 250/13 − 25/13 = 115/13.

**X4. Requerimiento §6.3: compras a tasas distintas y pago parcial.**
- A: 5.000 a 200 (`E = 25`). B: 8.000 a 220 (`E = 400/11`). Pago de 4.000 a 250.
  Hoy, 260.
- Cronológico: el pago retira A por 4.000.
  - **B = 4.000·(1/200 − 1/250) = 4,00**.
  - A potencial = (5 − 1.000/260) + (400/11 − 8.000/260) = 1,1538 + 5,5944 =
    **6,75**.
  - Total = **1.537/143 = 10,75**.
- Si el pago retirara B primero: realizado 2,18, potencial 8,57, total
  **1.537/143** ✓.

**X5. Beneficio económico confirmado (C).**
- Compra de 10.000 a 200 (`E = 50`).
- Pago mediante FX_EXCHANGE de **USD 38,00 → VES 10.000** directo a la TDC. La
  tasa de referencia del día es 250 (`S = 40`).
- **B = 10,00** (valoración con la tasa de referencia). **C = 50 − 38 = 12,00**
  (con lo efectivamente entregado).
- Los 2,00 de diferencia se deben a la ejecución frente a la referencia
  (10.000/38 ≈ 263,16) y no se mezclan con B.
- Si el mismo pago se hubiera hecho desde una cuenta VES: C = "no atribuible:
  Bs. 10.000", contado y no estimado.

**X6. Devolución.**
- Compra de 10.000 a 200 (`E = 50`). Devolución de 10.000 a 250 (`S = 40`).
- Efecto en devoluciones = 10, **fuera de B, C y D**.
- **Por qué:** M6 valora el gasto en −50 y la devolución en +40, cada uno en su
  día (TD-068). El flujo de caja ya muestra un gasto neto de 10 que es pura
  valoración. En el patrimonio, −50 + 10 (depreciación) + 40 = 0.
- Reportarlo como beneficio sería atribuir ganancia a una operación que
  económicamente se anuló.

**X7. Saldo a favor.**
- Se pagan de más 2.000 a 250 (`E = 8`). Los consume una compra de 2.000 a 260
  (`S = 7,6923`).
- Efecto sobre el saldo a favor = **−0,31**, en su propia línea y fuera de D.

#### R3.3 Separación formal de componentes

- El principal de compras, el resto del principal, los intereses, las comisiones
  y las penalizaciones se presentan como líneas propias en A, B y D.
- Las devoluciones y el saldo a favor tienen sus propias líneas.
- Los pagos no son componentes: son el *evento* que retira componentes. Cada pago
  lista lo que retiró, con su traza.

### R4. Integración con M7

#### R4.1 Tres conceptos que nunca se confunden

| Concepto | Definición | Ejemplos |
| --- | --- | --- |
| **Ingreso contable** | Transacción `INCOME`. Cuenta en el flujo de caja de M6 y M6.5. | Salario, cashback |
| **Ingreso elegible** (M7) | Ingreso contable **acreditado en una cuenta de naturaleza ASSET** que además cumple las reglas de M7 (política vigente, *override*, categoría, tasa > 0). | Salario en el banco |
| **Movimiento reductor de pasivo** | Cualquier crédito sobre una cuenta LIABILITY. Reduce una deuda y **no crea liquidez disponible**. | Pago, devolución, cashback en la TDC |

#### R4.2 Regla estable de elegibilidad (`PROPOSED`, TD-094)

> Un ingreso es elegible si y solo si **(1)** es `INCOME`, **(2)** su movimiento
> aumenta una cuenta de naturaleza **ASSET**, y **(3)** pasa las reglas existentes
> de M7, en su orden actual.

- La condición (2) se evalúa **antes** que los *overrides*. Una inclusión manual
  no puede volver elegible un ingreso acreditado a un pasivo, porque produciría
  una reserva sin respaldo.
- La naturaleza se deriva del tipo de cuenta en Go (`account.Type.Nature()`).
  Nunca se infiere de la categoría ni del signo.
- **Implementación:** es un paso nuevo al inicio de `rules.decide`
  (`internal/savings/suggest.go:78`), con su motivo legible ("Se acreditó a «X»,
  que es un pasivo: reduce una deuda y no aporta fondos disponibles").
  `ledger.IncomeItem` gana `AccountType`.

#### R4.3 Respaldo de reservas (TD-083 reforzada)

- El respaldo es la suma de `max(0, saldo)` de las **cuentas USD de naturaleza
  ASSET**.
- Hay que cambiar estas funciones de M7:
  - `fundTx` (`internal/savings/fund.go:200-240`): excluir los pasivos del total,
    de lo libre y de la tabla por cuenta;
  - `checkFundAccount` (`fund.go:433-448`): rechazar un pasivo con
    `ErrLiabilityAccount`;
  - `AccountImpact` (`integration.go:97-129`): `Applies = false` para un pasivo;
  - `ApplyExpensePlanTx` y `ApplyTransferPlanTx`: rechazar un plan con un extremo
    pasivo.
- TD-086 sigue intacta: **la transacción nunca se rechaza por el ahorro**. El
  formulario no ofrece un plan que tenga un extremo pasivo; si llega uno fabricado,
  es un error 422 *del plan*.

#### R4.4 Casos obligatorios

| Caso | ¿Ingreso contable? | ¿Elegible? | Efecto en el pasivo | Efecto en M7 | ¿Riesgo de reserva sin respaldo? |
| --- | --- | --- | --- | --- | --- |
| **Cashback acreditado a la TDC** (INCOME en la TDC) | Sí | **No** (regla 2) | Reduce la deuda | Sin sugerencia. Si un ingreso existente se mueve a la TDC, su revisión viva queda `INVALIDATED`. Las reservas confirmadas **no se tocan** y M7 reporta `OVER_CONFIRMED` (`issues.go:126`). | No |
| **Devolución de una compra** (REFUND) | No: reduce gasto | No: no es INCOME | Reduce la deuda (TDC) o aumenta el saldo (banco) | Ninguno. Si la compra original consumió reservas (`SPEND`), la devolución **no** las restituye sola; el usuario puede reservar de nuevo a mano. Se documenta. | No |
| **TDC USD con saldo a favor** | — | — | Saldo positivo en un pasivo | Excluida del fondo, de lo libre, del respaldo, de `AccountImpact` y de los planes | No: antes de M8.1 sí lo era, por eso este cambio entra **en M8.1** |
| **Pago desde una cuenta USD** (FX_EXCHANGE USD→TDC) | No | No | Reduce la deuda | Baja el saldo USD del origen. Si queda por debajo de lo reservado, M7 reporta `OVERALLOCATED` (comportamiento existente). El formulario muestra antes el impacto (`AccountImpact` del origen). Es la **evidencia D-4** para C. El formulario de pago ofrece consumir esas reservas en la misma operación (D-12, M8.2). | No |
| **Pago desde una cuenta VES** (TRANSFER VES→TDC) | No | No | Reduce la deuda | Ninguno: M7 es solo USD y `ApplyTransferPlanTx` exige USD en ambos lados | No |

#### R4.5 Historia de M7

- Antes de M8 no existe ninguna cuenta LIABILITY. La regla (2) y el filtro de
  respaldo **no cambian ninguna sugerencia, reserva ni incidencia existente**.
- Se prueba con un *fixture* de M7: `Overview`, `PlanSync` e `Issues` dan
  resultados idénticos antes y después de las migraciones y del cambio de reglas.
- Las operaciones de M7 siguen siendo append-only, y M8 no escribe en `savings_*`.

### R5. Invariantes contables

| # | Invariante | Cómo se garantiza |
| --- | --- | --- |
| I1 | `creditanalytics` **nunca** modifica el patrimonio, no escribe nada y no produce ingreso, gasto ni movimiento. | El paquete no importa ningún repositorio de escritura (prueba de imports). Prueba: el patrimonio y la serie son idénticos con y sin calcular la analítica. |
| I2 | Patrimonio = Σ saldos valorados = activos − pasivos. | Las líneas por cuenta se reparten por resto mayor y `AssetsMinor − LiabilitiesMinor == TotalMinor` exacto. |
| I3 | Una compra con TDC no toca ninguna cuenta activo. | Una sola fila en `account_movements`, en la TDC. |
| I4 | Un pago suma cero en su moneda (TRANSFER) o es un FX_EXCHANGE. Nunca es gasto ni ingreso. | Tipos existentes y TD-068. |
| I5 | La asignación de un pago es una partición exacta de su importe. | Prueba de propiedad: Σ asignaciones = importe, para cualquier secuencia de hechos. |
| I6 | El efecto total (`A + B` y `D_realizado + D_estimado`) no depende de la política de orden. | Prueba de propiedad entre `COSTS_FIRST` y `CHRONOLOGICAL`. |
| I7 | Cada costo entra en D una sola vez, a su valor `S`. | R3.1 y pruebas de las formas erróneas. |
| I8 | Nada calculado se persiste: asignaciones, estados de ciclo y métricas. | Revisión del esquema: solo hechos y aserciones. |
| I9 | Una tasa faltante nunca es cero ni una tasa posterior. | `Missing` + `Complete = false` (TD-067). |
| I10 | Ningún ingreso en un pasivo es elegible, y ninguna reserva se respalda con un pasivo. | R4.2 y R4.3. |
| I11 | Un doble envío no duplica nada. | `operation_requests` (§4.2). |

### R6. Migración D-2A: condiciones verificadas

**Verificado en el código y en la documentación oficial:**

1. **El PRAGMA va fuera de la transacción.**
   - "This pragma is a no-op within a transaction"
     ([pragma.html#pragma_foreign_keys](https://www.sqlite.org/pragma.html#pragma_foreign_keys)).
   - La migración `00019` se marca `-- +goose NO TRANSACTION`, así que goose no
     abre ninguna transacción. Ejecuta `PRAGMA foreign_keys=OFF` →
     `BEGIN IMMEDIATE` → … → `COMMIT` → `PRAGMA foreign_keys=ON`.
   - `BEGIN IMMEDIATE` es explícito porque `_txlock` solo afecta a `BeginTx`.
2. **Comportamiento real de goose v3.27.3.**
   - `initialize` toma **un** `*sql.Conn` con `p.db.Conn` (`provider_run.go:279`).
   - Una migración SQL sin transacción ejecuta cada sentencia con `ExecContext`
     sobre ese mismo `Conn` (`runIndividually`, `provider_run.go:203-245`;
     `runSQL`, `:510`).
   - El pool está fijado a 1 conexión (`internal/database/sqlite.go:152`), así que
     los PRAGMA y el `BEGIN` caen en **la** conexión.
   - goose inserta la versión *después*, en el mismo `Conn` y fuera de nuestra
     transacción.
   - Una migración **Go** sin transacción usaría `*sql.DB` y se bloquearía con 1
     conexión (comentario en `provider_run.go:221-227`). Por eso es SQL, como
     exige `CLAUDE.md`.
3. **Procedimiento.** Se siguen los 12 pasos oficiales
   ([lang_altertable.html §8](https://www.sqlite.org/lang_altertable.html)): crear
   `accounts_new` y `transactions_new`, copiar, `DROP` de la vieja y
   `RENAME new → X`.
   - **No** se renombra la vieja primero: la documentación advierte que eso
     "might corrupt references to that table in triggers, views, and foreign key
     constraints".
   - Desde 3.26.0 las referencias FK se reescriben al renombrar salvo con
     `legacy_alter_table=ON`. La migración lo verifica (punto 5).
4. **Índices, triggers, vistas y referencias.**
   - Se recrean `idx_accounts_currency_code` y los 3 índices de `transactions`.
   - No hay triggers ni vistas en `00001`–`00018` (verificado con grep). Una prueba
     compara `sqlite_schema` antes y después: solo puede cambiar el `sql` de las
     dos tablas, y cada índice, trigger y vista conserva su nombre y su SQL.
   - Las tablas hijas, incluidas las de M7, **no se reconstruyen**. Su
     `REFERENCES accounts(id)` / `transactions(id)` sigue apuntando por nombre.
5. **Guardas dentro de la transacción.** Un SQL puro no puede ramificar, así que
   cada guarda es un `INSERT` en una tabla `TEMP` con `CHECK`. Si falla, la
   sentencia falla y la migración se aborta:
   - `pragma_legacy_alter_table = 0` antes de renombrar;
   - conteos iguales entre la tabla vieja y la nueva **antes** del `DROP`;
   - `SELECT count(*) FROM pragma_foreign_key_check` = 0 antes del `COMMIT`;
   - `pragma_integrity_check` = 'ok'. Se usan ambos porque "integrity_check does
     not find FOREIGN KEY errors".
6. **Ensayo real** con `modernc.org/sqlite` v1.57.0, en un programa desechable
   del *scratchpad* (no está en el repo):
   - **Éxito:** `foreign_keys` vuelve a 1, el `REFERENCES` del hijo queda intacto,
     `integrity_check = ok`, un huérfano posterior se rechaza y el valor nuevo del
     enum se acepta.
   - **Guarda disparada:** la sentencia falla con `CHECK constraint failed`. **La
     conexión queda con `foreign_keys=0` y una transacción abierta.** Al cerrar el
     `*sql.DB` y reabrir, el esquema y los datos están **intactos** (rollback) y
     `foreign_keys=1`.
7. **`foreign_keys` vuelve a estar activo incluso ante errores.**
   - Si falla una sentencia, `migrations.Up` devuelve el error y `cmd/web` cierra
     el `*sql.DB` (`cmd/web/main.go:144-145`). Así se descartan la transacción y la
     conexión con FK=0 (verificado en el punto 6).
   - Se documenta en `migrations.Up`: "tras un error el `*sql.DB` no es
     reutilizable". Una prueba lo exige.
   - Si todo sale bien, `migrations.Up` comprueba la postcondición
     `PRAGMA foreign_keys = 1` mediante una función de `internal/database` (dueño
     de los PRAGMA), por ejemplo `database.CheckForeignKeys`. Si no se cumple,
     falla el arranque.
8. **Re-ejecución segura.** Si el proceso muere entre nuestro `COMMIT` y el
   `INSERT` de versión de goose, el siguiente arranque vuelve a ejecutar `00019`.
   El cuerpo es idempotente: reconstruye a partir del estado actual y produce el
   mismo esquema. Una prueba lo ejecuta dos veces.
9. **Upgrade desde datos de M7.** Prueba `18 → latest` con sugerencias, legs,
   operaciones, entradas, metas e *overrides* que referencian `accounts` y
   `transactions`. Comprueba conteos, `foreign_key_check` vacío y `Overview`
   idéntico.
10. **Restauración.**
    - `backup.Validate` (`internal/backup/service.go:216`) hoy corre
      `integrity_check` más las tablas esperadas para la versión. **Propuesta:**
      añadir `foreign_key_check`, porque `integrity_check` no lo cubre.
    - Pruebas:
      - un backup v18 (pre-M8) se valida, se restaura y en el siguiente arranque
        toma un snapshot previo a la migración y migra a la última versión;
      - un backup posterior a M8 también se restaura.
    - Ensayo manual sobre una copia de la base real antes del merge.
11. **TD-088:** excepción acotada a TD-037. Las reconstrucciones de una tabla
    **padre** usan el procedimiento oficial de 12 pasos con estas guardas. TD-037
    sigue valiendo como alternativa cuando el padre no tiene hijos fuera de la
    migración.

### R7. Fechas de las transacciones (D-8)

- **Investigación.** No encontré documentación pública de Banco Plaza que diga si
  el ciclo se asigna por fecha de operación o de contabilización. Lo general es
  que el corte cierra el registro de movimientos *procesados* hasta ese día. Para
  Plaza es **`UNKNOWN`**. Se confirmará con el primer estado de cuenta real.
- **Solución:**
  1. `effective_date` es **siempre la fecha de operación**. Gobierna la tasa
     (R3), la historia del patrimonio (TD-065) y el flujo de caja.
  2. La **pertenencia a un ciclo** se *deriva* de esa fecha con el calendario
     (§5.4) y nunca se guarda.
  3. Cuando el banco incluyó un movimiento en otro estado de cuenta (por un
     desfase de contabilización), el usuario lo **declara** en la pantalla de
     conciliación: `credit_statement_assignments(transaction_id, statement_id)`.
     - Restricción en Go: el corte del estado asignado tiene que ser
       `≥ effective_date`, porque nada se factura antes de ocurrir.
     - Sirve igual para débitos y para pagos: un pago del día del corte que el
       banco procesó al día siguiente.
  4. **No** se añade `posted_date`. La única consecuencia contable de la fecha de
     contabilización es la pertenencia a un ciclo, y eso es lo que se declara
     directamente. Una fecha sería un sustituto indirecto.
- **M8.1** no necesita nada de esto: las compras se registran con su fecha
  original y nada queda bloqueado, porque la pertenencia no se almacena. La tabla
  llega en M8.2.

### R8. Conciliación con el banco (M8.2)

- **Por estado de cuenta:** `Δ_j = B_j − L_j` (con pertenencia). Si es distinta de
  cero, la UI lista los candidatos: movimientos cerca del corte, cargos sin
  registrar.
- **Cifras opcionales que reporta el banco** en `credit_statements`:
  `previous_balance_minor`, `payments_minor`, `debits_minor`, `past_due_minor`.
  Con ellas, `Δ` se descompone en pagos, débitos y saldo anterior.
- `past_due_minor` permite verificar la hipótesis de anidamiento del mínimo
  (R2.3), y la variación del `B` siguiente verifica la `refund_policy` (R2.5).
- La conciliación **informa; nunca corrige sola**.

### R9. Decisiones técnicas nuevas (numeración tras M7)

| TD | Contenido |
| --- | --- |
| TD-088 | Reconstrucción de un padre con el procedimiento de 12 pasos y guardas (R6) |
| TD-089 | Naturaleza de cuenta ASSET/LIABILITY derivada del tipo; `CREDIT_CARD`; la naturaleza no se puede cambiar. Reemplaza "No credit". |
| TD-090 | Tipo `REFUND` |
| TD-091 | El pago de TDC es una TRANSFER o un FX_EXCHANGE; la etiqueta se infiere |
| TD-092 | La asignación es derivada; solo se persisten hechos y aserciones (R2.2) |
| TD-093 | Efecto cambiario analítico sobre pasivos nominales: componentes, fórmulas e invariantes I1/I6/I7. **Reemplaza en parte** "No realized or unrealized FX gains", solo para explicar pasivos; nunca como ingreso ni gasto. |
| TD-094 | Elegibilidad de M7 por naturaleza de la cuenta; respaldo solo con activos |
| TD-095 | Idempotencia con `operation_requests` |
| TD-096 | Fechas de compra y pago = hechos de calendario con reglas fijas; simulación rotulada. **Precisa** "No recommendations / forecasting". |

### R10. Riesgos residuales

| # | Riesgo | Mitigación | Residual |
| --- | --- | --- | --- |
| RR1 | Plaza no anida el mínimo vencido | `past_due_minor` y alerta de conciliación | Bajo |
| RR2 | Orden de imputación de Plaza ≠ `COSTS_FIRST` | Configurable. El total no cambia (I6); solo la partición realizado/potencial. | Bajo |
| RR3 | Asignaciones históricas que cambian tras una compra retroactiva | Es lo correcto con H, y se muestra la traza. Las cifras del banco son fijas porque son hechos. | Aceptado |
| RR4 | Falla de la migración 00019 | Guardas, rollback verificado, backup automático, re-ejecución idempotente | Bajo |
| RR5 | No existen tasas BCV históricas para compras antiguas | `Missing` con enlace a la carga manual | Medio (dato) |
| RR6 | Tarjeta indexada registrada como NOMINAL | `denomination_confirmed`; el motor rechaza INDEXED | Medio (depende del usuario) |
| RR7 | Un USD reservado se usa para pagar la TDC | Plan de ahorro en el formulario de pago (D-12); sin plan, `OVERALLOCATED` de M7 | Bajo |
| RR8 | Las cifras de M6 y M6.5 cambian con los REFUND | Documentado y probado | Bajo |

### R11. Impacto en el alcance

| Sub | Cambios por el review |
| --- | --- |
| **M8.1** | **Absorbe el guard de pasivos de M7** (R4.2 y R4.3) y sus regresiones, porque desde M8.1 existen cuentas pasivo. También: guardas y pruebas de R6, y `database.CheckForeignKeys`. |
| **M8.2** | Crece: motor de obligaciones (capa O) con traza, `credit_statement_assignments`, cifras del banco opcionales, `refund_policy`, pantalla de conciliación. |
| **M8.3** | Capa C con `imputation_order`; separación formal de componentes; líneas de devoluciones y saldo a favor; fórmulas R3. |
| **M8.4** | Ya no incluye el guard de M7 (pasó a M8.1). Liquidez, vista previa de impacto en pagos USD y suite completa de regresión M7. |
| **M8.5** | Sin cambios. |

### R12. Casos de prueba añadidos

- **Obligaciones:**
  - E1–E7 completos;
  - propiedad I5 (partición exacta) con secuencias generadas;
  - umbrales acotados con advertencia;
  - `V` como máximo, no suma;
  - mínimo desconocido;
  - sin estado vigente;
  - corte editado frente a una aserción posterior (rechazado);
  - aserción a un corte anterior a la fecha (rechazada).
- **Devoluciones:**
  - `NEXT_STATEMENT` frente a `REDUCES_BILLED`;
  - nunca cuentan para el mínimo ni el vencido;
  - vinculada frente a no vinculada en la capa C;
  - devolución mayor que la compra rechazada.
- **Cambiario:**
  - X1–X7 exactos;
  - formas erróneas (i) y (ii) detectadas;
  - I6 entre políticas;
  - I1: patrimonio y serie idénticos;
  - C solo con FX_EXCHANGE desde USD, y "no atribuible" contado con VES;
  - efecto en devoluciones fuera de D;
  - tasa faltante en la fecha del componente o del retiro, que da `Missing`;
  - tasa corregida que se recalcula y cambia las *legs*.
- **M7:**
  - los cinco casos de R4.4;
  - un *override* incluyente sobre una cuenta pasivo sigue siendo no elegible;
  - un ingreso movido a la TDC da `INVALIDATED`, reservas intactas y
    `OVER_CONFIRMED`;
  - el *fixture* M7 idéntico antes y después;
  - un plan con un extremo pasivo da 422 y la transacción **no** se rechaza
    (TD-086).
- **Migración:**
  - las 11 condiciones de R6;
  - falla inyectada en cada guarda → base intacta tras reabrir;
  - el cuerpo ejecutado dos veces;
  - `sqlite_schema` comparado;
  - restauración de v18 y de la última versión.
- **Fechas:**
  - compra el día del corte con y sin aserción;
  - pago procesado al día siguiente del corte;
  - 31 en febrero;
  - cambio de año con aserción.

---

## 0. Hallazgos de la auditoría (rev. 1, vigentes)

1. **El ledger ya modela pasivos sin cambiar su fórmula.**
   - La deuda es un saldo negativo, y la valoración suma por moneda y convierte una
     vez (`internal/valuation/snapshot.go:310`).
   - Ninguna consulta del ledger, la valoración ni la analítica filtra por
     `account_type`.
2. **No hay forma de registrar una devolución.** Un EXPENSE siempre es negativo
   (`internal/ledger/service.go:389`), así que hace falta `REFUND`.
3. **M7 trata cualquier cuenta USD como respaldo de reservas**, y cualquier
   `INCOME` como elegible. Resuelto en R4.
4. **`account_type` y `transaction_type` son `CHECK` en tablas padre.** Resuelto
   en R6.
5. **No hay idempotencia en los formularios.** M7 introdujo `request_key`, y M8 lo
   generaliza.
6. **`CLAUDE.md` prohíbe hoy parte de M8.** Resuelto en R9.
7. **Observado fuera de M8:**
   - `transfers.go:207` y `transactions.go:344` aceptan `return_to` con `//host`
     (posible *open redirect*). **Resuelto en M8.1:** todo `return_to` pasa por
     `safeReturn`, que solo acepta una ruta local;
   - los errores enviados desde el móvil se renderizan en la plantilla de
     escritorio.

## 1. Estado actual del sistema

| Pieza | Qué hace hoy | Archivo |
| --- | --- | --- |
| `accounts` | Tipos `BANK/CASH/WALLET/OTHER`. Moneda inmutable; tipo editable. Sin columna de saldo. | `internal/account/account.go` |
| `transactions` + `account_movements` | Un evento y N movimientos con signo. Tipos: `INCOME`, `EXPENSE`, `OPENING_BALANCE`, `TRANSFER`, `FX_EXCHANGE`. | `internal/ledger/ledger.go:191` |
| Saldos | Siempre a una fecha; "actual" = hoy en Venezuela (TD-063) | `internal/ledger/repository.go` |
| Tasas | Última observación `≤ fecha`; MANUAL > BCV dentro de una misma fecha; un pivote; resolver único `fx.Book` | `internal/fx/` |
| Valoración | `Snapshot` en memoria; nada se guarda | `internal/valuation/` |
| Analítica | Flujo de caja = INCOME/EXPENSE a la tasa de su día (TD-068); recap | `internal/analytics/` |
| M7 | Política, sugerencias congeladas, fondo de operaciones y entradas, metas, incidencias derivadas, hooks en `application` | `internal/savings/`, `internal/application/` |
| Web | `net/http`, PRG 303, 422/409, htmx solo en GET, *shapes* móviles en el servidor | `internal/web/` |
| Backups | `VACUUM INTO` antes de migrar; `Validate` = `integrity_check` + tablas esperadas | `cmd/web/main.go:322`, `internal/backup/` |

## 2. Análisis de impacto

| Área | Cambio | Submilestone |
| --- | --- | --- |
| `account` | `CREDIT_CARD`, `Nature()`, guard de naturaleza (TD-045) | M8.1 |
| `ledger` | `REFUND`; `IncomeItem.AccountType`; lecturas de actividad de pasivos | M8.1 |
| `valuation` | `AssetsMinor`/`LiabilitiesMinor`; `Share` solo sobre activos | M8.1 |
| `analytics` | REFUND como gasto negativo en su categoría | M8.1 |
| `savings` (M7) | Elegibilidad y respaldo por naturaleza (R4) | **M8.1** |
| `creditcard` | Configuración (M8.1); estados de cuenta, calendario y capa O (M8.2) | M8.1–2 |
| `creditanalytics` | Capa C y motor cambiario (M8.3); liquidez (M8.4) | M8.3–4 |
| `migrations` | 00019–00023 | M8.1–3 |
| `backup` | `foreign_key_check` en `Validate` | M8.1 |
| Web | Tipo de cuenta, grupos Activos/Pasivos, pantallas de TDC | M8.1–5 |

## 3. Arquitectura

```
HTTP (internal/web)
  └─ application ─ escrituras atómicas: abrir TDC, compra, cargo, pago, devolución, aserciones
        ├─ ledger · account · creditcard · (savings: hooks existentes de M7)
  └─ creditcard.Service ─ posición, ciclos, capa O (obligaciones), conciliación
  └─ creditanalytics ─ lectura entre dominios: capa C, motor cambiario, liquidez
        ├─ valuation.Snapshot / fx.Book · ledger · creditcard · savings (solo lectura)
```

- `creditcard` es el dominio. `creditanalytics` es una lectura entre dominios
  (como `valuation`) y no escribe nada.
- `application` solo coordina escrituras que cruzan dominios.
- `Nature()` deja preparado `LOAN` para un milestone futuro. **No se construye
  nada de préstamos.**
- Sin dependencias nuevas, sin interfaces de repositorio, sin ORM. Solo `math/big`
  y `money.Decimal`.

## 4. Modelo de datos

### 4.1 Tablas existentes

- `accounts.account_type`: se agrega `'CREDIT_CARD'`. El subtipo queda NULL.
  Nombre, institución, moneda y estado ya existen y **no se duplican**.
- `transactions.transaction_type`: se agrega `'REFUND'`, con
  `category_id` y `payment_method_id` NOT NULL (categoría EXPENSE, verificada en
  Go).
- **No** se agregan columnas a `transactions` ni a `account_movements`.

### 4.2 Tablas nuevas (`STRICT`, FK `RESTRICT`, timestamps con el patrón existente)

| Tabla | Migración | Columnas clave y restricciones |
| --- | --- | --- |
| `credit_cards` | 00020 (M8.1) | `account_id` PK → `accounts`; `product_name` NULL (1–120); `credit_limit_minor` NULL `> 0`; `statement_day` 1–31 (se usa el último día del mes si no existe); `due_days_after_statement` NULL 1–60; `payment_processing_days` 0–10; `denomination IN ('NOMINAL','INDEXED')`; `denomination_confirmed IN (0,1)`; `default_payment_method_id` NULL → `payment_methods` |
| `credit_charges` | 00021 (M8.1) | `transaction_id` PK → `transactions`; `kind IN ('INTEREST','FEE','PENALTY')`. En Go: EXPENSE sobre un pasivo. |
| `transaction_refunds` | 00021 (M8.1) | `refund_transaction_id` PK → `transactions`; `original_transaction_id` → `transactions`, con índice. En Go: el original es EXPENSE de la misma cuenta y la suma de devoluciones no supera el original. |
| `operation_requests` | 00021 (M8.1) | `request_key` PK (CHECK de M7: 16–64 `[0-9a-zA-Z_-]`); `transaction_id` → `transactions`. Se consulta primero con SELECT en la misma transacción; el PK es el respaldo. |
| `credit_statements` | 00022 (M8.2) | `id`; `card_account_id` → `credit_cards`; `period_start ≤ closing_date`; `due_date` NULL `> closing_date`; `closing_balance_minor` (convención del ledger); `minimum_payment_minor` NULL `≥ 0`; `previous_balance_minor`, `payments_minor`, `debits_minor`, `past_due_minor` NULL (cifras que reporta el banco). `UNIQUE(card_account_id, closing_date)`. El no solapamiento se verifica en Go. |
| `credit_statement_assignments` | 00022 (M8.2) | `transaction_id` PK → `transactions`; `statement_id` → `credit_statements`, con índice. En Go: el movimiento es de esa tarjeta y el corte es `≥ effective_date`. |
| `credit_cards.refund_policy` | 00022 (M8.2) | `ADD COLUMN ... NOT NULL DEFAULT 'NEXT_STATEMENT' CHECK (IN ('NEXT_STATEMENT','REDUCES_BILLED'))` |
| `credit_cards.imputation_order` | 00023 (M8.3) | `ADD COLUMN ... NOT NULL DEFAULT 'COSTS_FIRST' CHECK (IN ('COSTS_FIRST','CHRONOLOGICAL'))` |

Cada columna llega con el submilestone que la usa: no se crea nada antes de que
tenga código.

## 5. Modelo de transacciones

### 5.1 Representación

| Concepto | Transacción | Movimientos | Patrimonio | ¿Gasto? |
| --- | --- | --- | --- | --- |
| Compra | EXPENSE en la TDC | TDC −x | −x | Sí |
| Interés, comisión, penalización | EXPENSE + `credit_charges` | TDC −x | −x | Sí, y además costo financiero |
| Pago en la misma moneda | TRANSFER banco→TDC | −x / +x | 0 | No |
| Pago desde otra moneda | FX_EXCHANGE USD→TDC | −a / +b | Según la tasa | No |
| Devolución | REFUND (+ vínculo opcional) | TDC +x | +x | Reduce el gasto de su categoría |
| Avance de efectivo | TRANSFER TDC→banco | −x / +x | 0 | No (su comisión es FEE) |
| Deuda previa | OPENING_BALANCE negativo | TDC −x | — | No |
| Ajuste | No existe en M8 (D-6) | — | — | — |

- Los guards de edición **nombran lo que aceptan** (TD-045), también en la capa
  web: los cargos y las devoluciones vinculadas solo se editan por sus casos de uso.
- Cambiar la naturaleza de una cuenta se rechaza.

### 5.2 Casos de uso (`internal/application`)

`OpenCreditCard`, `RecordCardPurchase`, `RecordCardCharge`/`UpdateCardCharge`,
`RecordCardPayment` (TRANSFER o FX_EXCHANGE según la moneda de origen),
`RecordRefund`/`UpdateRefund` y `AssertStatementMembership` (M8.2).

- Todos reciben `RequestKey`, renderizado en el servidor.
- Todos corren en una sola transacción, con rollback probado por inyección de una
  falla.

### 5.3 Posición, ciclo y asignación

Se calculan con la capa O (R2.3).

### 5.4 Calendario

- `closing(m) = min(statement_day, último_día(m))`.
- Una fecha `x` pertenece al ciclo que cierra en `closing(mes(x))` si
  `día(x) ≤ closing`; si no, al del mes siguiente (esto cubre el cambio de año).
- Un estado de cuenta registrado prevalece en su rango, y una aserción prevalece
  sobre la regla.
- No hay tabla de feriados. Los días de procesamiento son hábiles de lunes a
  viernes, y se advierte que no se consideran los feriados.

## 6. Motor cambiario

Ver R2.6 y R3.

- Resolución de tasas:
  - política M5 sin cambios: `≤ fecha`, MANUAL > BCV dentro de una misma fecha,
    un pivote, nunca una tasa futura;
  - una tasa faltante da `Missing`;
  - se recalcula en cada lectura y se muestran las *legs* (D-3);
  - se cargan con un número constante de consultas mediante `valuation.Snapshot`
    (TD-065).
- Una tarjeta `INDEXED` se rechaza. Si la denominación no está confirmada, se
  muestra una advertencia.
- Contexto: el crédito indexado a la UVC está generalizado desde la Gaceta 43.298
  (enero de 2026). No está verificado si aplica a esta tarjeta: `UNKNOWN`.

## 7. Integración con M7

Ver R4.

## 8. API y UI

El patrón es el existente: `net/http` 1.22, PRG 303, 422/409/404, adaptador con
traducción de errores y fakes.

| Ruta | Uso | Sub |
| --- | --- | --- |
| `GET/POST /cards`, `/cards/new`, `/cards/{id}`, `/cards/{id}/edit` | Abrir, ver y configurar | 8.1 |
| `…/cards/{id}/purchases`, `…/charges`, `/refunds` | Compra, cargo, devolución | 8.1 |
| `…/cards/{id}/payments` | Pago, con importes sugeridos de vencido, mínimo y facturado; nunca se envía solo | 8.2 |
| `…/cards/{id}/statements`, `…/statements/{sid}/reconcile` | Estados de cuenta, conciliación y aserciones | 8.2 |
| `…/cards/{id}/fx` | Motor cambiario | 8.3 |
| `/liquidity` | Liquidez comprometida | 8.4 |
| `/mobile/tarjetas`, `/mobile/tarjetas/{id}` | Móvil | 8.1–8.5 |

- Panel de escritorio en la cuadrícula del dashboard (TD-071): LCD para la deuda,
  el ciclo, la analítica, la evolución (SVG) y los movimientos.
- Dashboard general con grupos **Activos/Pasivos**.
- Móvil funcional:
  - *shapes* `CARD_PURCHASE`/`CARD_PAYMENT` renderizadas en el servidor;
  - los errores vuelven a la plantilla móvil.
- Simulación de tasa rotulada "no es una proyección". "Fechas óptimas" son hechos
  de calendario (TD-096).

## 9. Plan de migraciones

| # | Contenido | Tipo | Sub |
| --- | --- | --- | --- |
| 00019 | Reconstrucción de `accounts` (+`CREDIT_CARD`) y `transactions` (+`REFUND`) con los 12 pasos y guardas (R6) | NO TRANSACTION, forward-only (`Down` falla a propósito) | 8.1 |
| 00020 | `credit_cards` | Reversible | 8.1 |
| 00021 | `credit_charges`, `transaction_refunds`, `operation_requests` | Reversible | 8.1 |
| 00022 | `credit_statements`, `credit_statement_assignments`, `refund_policy` | Reversible | 8.2 |
| 00023 | `imputation_order` | Reversible | 8.3 |

- **Rollback** de 00019: restaurar el snapshot automático previo a la migración.
  De 00020 en adelante: `Down` probado, que nunca corre automáticamente.
- `schema.go` (`tableIntroducedAt`) y las pruebas de esquema se actualizan en cada
  una.

## 10. Plan de pruebas

Se mantiene el plan de la rev. 1 y se le **suma R12**.

| Capa | Casos (resumen) |
| --- | --- |
| Migración | R6 completo; upgrade 13→N y 18→N con datos; restauraciones |
| Dominio | Compra, pagos (total, parcial, mínimo, excesivo, con varios componentes), cargos, devoluciones, retroactivas, cambio de ciclo y de año, corte 25, febrero y bisiesto, vencimiento desconocido |
| Obligaciones | E1–E7, I5 |
| Cambiario | X1–X7, I1, I6, I7, tasa faltante, tasa en fin de semana, MANUAL vs BCV, tasa futura, tasa corregida, INDEXED |
| Idempotencia y atomicidad | Doble envío, concurrencia con `BEGIN IMMEDIATE`, rollback inyectado |
| M7 | R4.4, R4.5, TD-086 |
| Web | 422/409/303, *shape* móvil con enlace directo, plantillas sin aritmética |

Puertas de calidad: `gofmt -l .`, `go vet ./...`, `go test ./... -count=1`, y
`-race` en el contenedor.

## 11. Decisiones pendientes y datos `UNKNOWN`

- **Requieren tu respuesta:**
  - ~~D-11~~: aprobado como provisional (`COSTS_FIRST`), pendiente de confirmar con el contrato de Plaza;
  - ~~D-12~~: resuelto, plan de ahorro en el pago (M8.2);
  - la propuesta de añadir `foreign_key_check` a `backup.Validate`.
- **`UNKNOWN`** (no bloquean; se muestran como pendientes o se verifican con la
  conciliación):
  - vencimiento y límite de la Mastercard Black;
  - orden de imputación de Plaza;
  - fecha que usa Plaza para asignar el ciclo;
  - si el mínimo anida el vencido;
  - si las devoluciones reducen el facturado;
  - período de gracia;
  - si la tarjeta es nominal o indexada a la UVC;
  - días de procesamiento.

## 12. Plan por submilestone

Cada uno cierra cuando: las puertas de calidad están en verde, los documentos
están actualizados **en el mismo cambio** y tú lo validas funcionalmente.

### M8.1 — Credit Accounts & Liability Ledger

- **Archivos:**
  - `internal/migrations/00019`–`00021` y sus pruebas (R6);
  - `internal/database` (`CheckForeignKeys`);
  - `internal/backup` (`foreign_key_check`);
  - `internal/account` (`CREDIT_CARD`, `Nature`, guard);
  - `internal/ledger` (`REFUND`, `IncomeItem.AccountType`);
  - `internal/creditcard` (configuración);
  - `internal/application` (`OpenCreditCard`, compra, cargo, devolución,
    `RequestKey`);
  - `internal/valuation` (activos y pasivos);
  - `internal/analytics` (REFUND);
  - **`internal/savings`** (R4.2 y R4.3);
  - web: tipo de cuenta, formularios, grupos Activos/Pasivos;
  - docs: TD-088 a TD-091, TD-094, TD-095 y `CLAUDE.md`.
- **Aceptación:**
  - crear la Plaza Black (VES, corte 25, sin límite ni vencimiento, NOMINAL sin
    confirmar);
  - compras retroactivas: el banco no cambia, la deuda y el patrimonio sí, con la
    historia correcta;
  - devolución que reduce el gasto y no es elegible;
  - cargos clasificados;
  - un doble envío no duplica;
  - el *fixture* M7 idéntico;
  - una TDC USD con saldo a favor no respalda reservas;
  - las 11 condiciones de R6 en verde.

### M8.2 — Statements & Payments

- **Archivos:**
  - `internal/migrations/00022`;
  - `internal/creditcard` (calendario, estados de cuenta, capa O, conciliación,
    aserciones);
  - `internal/application` (`RecordCardPayment`, `AssertStatementMembership`);
  - web: pago, estados de cuenta, conciliación;
  - docs: TD-092.
- **Aceptación:**
  - E1–E7;
  - I5;
  - pago desde USD y desde VES;
  - un vencimiento desconocido nunca da OVERDUE;
  - calendario (25, año, febrero, bisiesto);
  - conciliación con aserciones.

### M8.3 — FX Benefit Analytics

- **Archivos:** `internal/migrations/00023`; `internal/creditanalytics` (capa C,
  A/B/C/D, devoluciones, saldo a favor, `Missing`, *legs*); web `/cards/{id}/fx`;
  docs: TD-093.
- **Aceptación:** X1–X7, I1, I6, I7, separación por componente, confirmado frente
  a estimado.

### M8.4 — Liquidity & M7 Integration

- **Archivos:** `creditanalytics.Liquidity`; vista previa del impacto M7 en pagos
  desde USD; suite completa de regresión M7; `/liquidity`.
- **Aceptación:**
  - el ejemplo 30.000 − 8.000 = 22.000;
  - USD libre = activos − reservado, usado una sola vez;
  - cobertura cruzada con la tasa y sus *legs*, rotulada como estimación;
  - ninguna transacción ficticia.

### M8.5 — Credit Dashboard & Optimization

- **Archivos:** panel completo de escritorio y móvil; evolución de la deuda;
  simulación; hechos de calendario (TD-096).
- **Aceptación:**
  - móvil operable de punta a punta;
  - sin vencimiento no hay sugerencia;
  - la simulación no persiste;
  - `data-fit`;
  - ninguna plantilla calcula.

**Orden:** M8.1 → M8.2 → (M8.3 ∥ M8.4) → M8.5.

---

### Fuentes externas

- SQLite, ALTER TABLE: <https://www.sqlite.org/lang_altertable.html>
- SQLite, PRAGMA: <https://www.sqlite.org/pragma.html>
- goose v3.27.3: `provider_run.go` (copia en la caché de módulos local)
- Contrato TDC, Venezolano de Crédito: <https://www.venezolano.com/contrato-tdc/>
- Gaceta 43.298 (UVC): <https://finanzasdigital.com/bcv-regula-tasas-interes-gaceta-oficial-43298/>
