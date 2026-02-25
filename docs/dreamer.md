# 🧠 Dreamer Cognitive & Memory Engine Architecture Whitepaper (MKCT Ultimate Edition v2)

## Module 1: Core Concepts & Mathematical Engine
**Design Principle: Everything is a Megram, flowing dynamically through the MKCT Pyramid via dual-channel convolution.**

### 1. The MKCT Pyramid
*   **M (Megram):** The fundamental, indivisible atomic particle of episodic memory. It records objective facts and GGS (Goal Gradient Solver) routing states at the exact moment of creation.
*   **K (Knowledge):** Short-term local cache for specific tasks. Highly susceptible to time-decay forgetting.
*   **C (Common Sense):** Cross-task general heuristics (SOPs). Emerges from the convolution integral of high-energy Megrams.
*   **T (Thinking):** The supreme constitution and persona of the system (includes the fundamental Agent Laws). Immutable and non-decaying.

### 2. Base Tuple (`Megram`)
Every critical event routed by GGS (or the Accept path) must be encapsulated into a strict 9-element tuple and persisted into the database:
$$ Megram_i = \langle ID, \text{Level}, t_i, s_i, ent_i, c_i, \mathbf{State}, \mathbf{f_i}, \mathbf{\sigma_i} \rangle $$
*   **`t_i`**: Timestamp (drives the decay kernel $g(\Delta t)$).
*   **`s_i, ent_i`**: Space & Entity serving as **Inverted Index Tags**.
    *   *Example 1 (Micro-event):* `s_i = "tool:ripgrep"`, `ent_i = "path:/src"`.
    *   *Example 2 (Terminal/Macro-event):* `s_i = "intent:db_migration"`, `ent_i = "env:prod"`.
*   **`c_i`**: Content (e.g., error logs, final summaries).
*   **`State`**: The routing macro-state (e.g., `change_approach`, `accept`).
*   **`f_i`**: Initial stimulus magnitude $[0, 1]$ (absolute energy).
*   **`sigma_i`**: Valence direction $[-1.0, +1.0]$ (continuous positive/negative feedback).

### 3. Definitions of Convolution Factors ($f$, $g$, and $k$)
*   **$f(\tau)$: Initial Stimulus Function (Magnitude)**
    *   Defines the "absolute energy" or "salience" of a Megram at creation ($[0, 1]$).
*   **$g(\Delta t)$: Time Decay Kernel Function**
    *   Defined as $g(\Delta t) = e^{-k \cdot \Delta t}$, where $\Delta t = t_{now} - \tau$ is the elapsed time in **Days**.
    *   **The Decay Rate $k$:**
        *   **$k = 0.0$ (Timeless):** Promoted $C$ and $T$ levels. Zero decay.
        *   **$k = 0.05$ (Slow Decay):** `abandon`, `accept`, `change_approach`, `break_symmetry`. Lingers for offline consolidation.
        *   **$k = 0.2$ (Medium Decay):** `change_path`.
        *   **$k = 0.5$ (Fast Decay):** `refine`.

### 4. The GGS Quantization Matrix
The parameters ($f_i$, $\sigma_i$, and $k_i$) are mathematically mapped from the GGS (or Accept) macro-state:

| State | Magnitude $f$ | Valence $\sigma$ | Decay $k$ | Physical Meaning & Planner Action |
| :--- | :--- | :--- | :--- | :--- |
| **`abandon`** | **0.95** (Extreme) | **-1.0** (Red Alert) | 0.05 | **PTSD Trauma.** High negative score generates a strict Constraint. |
| **`accept`** *(D=0)*| **0.90** (Extreme) | **+1.0** (Golden) | 0.05 | **Flawless Golden Path.** Task fully accomplished. Highly reinforced. |
| **`change_approach`** | **0.85** (High) | **-1.0** (Negative) | 0.05 | **Anti-pattern.** Planner adds this tool/method to the blacklist. |
| **`success`** *(Subtask)*| **0.80** (High) | **+1.0** (Positive) | 0.05 | **Best Practice (SOP).** Planner copies this behavior directly. |
| **`break_symmetry`** | **0.75** (Med-High)| **+1.0** (Positive) | 0.05 | **Spark of Inspiration.** Planner favors retrying this breakthrough point. |
| **`change_path`** | **0.30** (Low) | **0.0** (Neutral) | 0.2 | **Objective Dead-End.** $\sigma=0$ ensures the tool's reputation is unharmed. Immediate path avoidance is handled by GGS `blocked_targets`. |
| **`refine`** | **0.10** (Weak) | **+0.5** (Slight Pos) | 0.5 | **Muscle Memory.** Fast GC. |

### 5. Core Engine: The Dual-Channel Convolution Potential
We separate "Attention" from "Preference" using time-integrated convolutions to prevent positive and negative experiences from silently canceling each other out.

*   **Channel A: Attention Potential** — *Decides "Where to Look"*
    $$ M_{attention} = \sum |f_i| \cdot e^{-k_i \cdot \Delta t} $$
    *   High scores ensure the entity remains firmly on the system's radar.
*   **Channel B: Decision Potential** — *Decides "What to Do"*
    $$ M_{decision} = \sum \sigma_i \cdot f_i \cdot e^{-k_i \cdot \Delta t} $$
    *   $>0$ triggers **Exploit**; $<0$ triggers **Avoid**. If $M_{att}$ is massive but $M_{dec} \approx 0$, it triggers **Caution** (Sandboxing required for high-variance tools).

---

## Module 2: Online Synchronous Flow
**Design Principle: Extremely lightweight I/O; never block the main business logic.**

### 1. Generation: Fire-and-Forget Asynchronous Write (GGS $\to$ Memory)
When a routing decision is made, the system instantiates a `Megram` and issues a **fire-and-forget asynchronous write** (e.g., via Goroutines) to append it to LevelDB, ensuring the hot path is never blocked.

### 2. MKCT Cascading Load & The Data Interface (Planner $\leftarrow$ Memory)
*   **Interface Decoupling:** The Memory module acts strictly as a Data Service. It returns **Structured Data** (e.g., `[]SOPRecord`, `map[Entity]Potentials`). The Planner (R2) is responsible for formatting this data into LLM prompts.
*   **Load Strategy:**
    *   **T-Layer:** Hardcoded in the `Global_System_Prompt`.
    *   **C-Layer:** Retrieves matched SOPs based on task Tags.
    *   **M/K-Layer:** Calculates $M_{attention}$ and $M_{decision}$ dynamically at $t_{now}$ (Lazy Evaluation).

### 3. Write-back & Recall
Querying a memory updates `last_recalled_at` (resets time decay). If quoting an old common sense rule leads to a GGS error, the system asynchronously appends a new negative $\sigma$ Megram.

---

## Module 3: Offline Asynchronous Flow (Dreamer)
**Design Principle: Background coroutines running during system idle times for cognitive evolution.**

### 1. Consolidation Engine (Upward Flow)
*   **Clustering & Convolution:** Scans Megrams with identical Tags, calculating the total dual-potential.
*   **Promotion (The $\Lambda_{rule}$ Threshold):** 
    *   If $M_{attention} \ge 5.0$ AND $M_{decision} \ge 3.0$: Promote to **Best Practice** (C-Level).
    *   If $M_{attention} \ge 5.0$ AND $M_{decision} \le -3.0$: Promote to **Absolute Constraint** (C-Level).
    *   The engine invokes the LLM to distill a generalized rule, generating a new $Megram(Level=C, k=0.0)$ with time immunity.

### 2. Degradation & GC Engine (Downward Flow)
*   **Trust Bankruptcy (The $\Lambda_{demote}$ Threshold):** Scans $C$-level common sense. If $M_{decision} < 0.0$ (due to recent negative feedback), it strips the time immunity ($k \to 0.05$) and forcibly demotes it.
*   **Physical Forgetting (The $\Lambda_{gc}$ Threshold):** Scans $M$ and $K$. If exponential time decay causes $M_{attention} < 0.1$, the engine issues a hard `DELETE` from LevelDB.

---

## Module 4: Coordination Mechanisms (MVP Scope)
**Design Principle: Runtime dynamic overwrite to resolve immediate conflicts.**

### 1. Dual-Axis Priority
*   **Bottom-line Conflict ($T > C/K/M$):** Core values and Agent Laws have absolute veto power. 
*   **Factual Conflict ($M > C$):** When the latest objective reality ($M$) conflicts with old experience ($C$), the system must **trust the latest reality at runtime (Soft Overwrite)**. 

---

## Module 5: Infrastructure & Non-Functional Requirements

### 1. Storage Selection: LevelDB
The architecture uses **LevelDB** (a Key-Value store with inverted indexing) instead of Vector Databases. LevelDB's underlying **LSM-Tree** architecture provides unparalleled sequential write throughput for Append-Only I/O operations, perfectly matching our Event Sourcing design.

### 2. Robustness: Event Sourcing
To ensure absolute system stability, all online operations are **Append-Only**. The system never uses `UPDATE` to modify a past Megram. Error correction is achieved via mathematical cancellation: appending reverse Megrams (Negative $\sigma$) to neutralize outdated positive potentials dynamically during convolution.

***

## Module 6: Phase 2 Roadmap (Advanced Cognitive Features)
*These features are decoupled from the MVP to maintain architectural simplicity.*

### 1. Schema Transfer Engine (Innovation & Generalization)
*   **Mechanism:** The Dreamer will perform Semantic Factorization on high-scoring Megrams, identify Semantic Anchors (e.g., `-s` means "summary"), and generate "Hypothesis Megrams" to invent new tool combinations (e.g., fusing `ls -s` with `du -h` to invent `du -sh`).

### 2. Cognitive Dissonance & Dissonance Megrams
*   **Mechanism:** Whenever a "Soft Overwrite" occurs at runtime (Module 4), the system will generate a special `Dissonance Megram` (High $f$, Negative $\sigma$). During the nighttime Dreamer cycle, this concentrated negative energy will mathematically shatter the credibility ($M_{decision}$) of the outdated SOP, ensuring it is permanently demoted.
