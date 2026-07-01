/**
 * Trace types for the Melange Explain API.
 *
 * Mirrors the Go runtime types in `melange/trace.go`. The PostgreSQL
 * functions return JSONB shaped as snake_case (matching SQL column names);
 * the TypeScript field names follow the same convention so the wire
 * payload deserialises with `JSON.parse` and no manual remapping. The
 * camelCase preference of TypeScript consumers can be applied by mapping
 * at the call site if desired.
 */
import type { ObjectType, Relation } from './types.js';

/**
 * NodeType discriminates the variants of a Node in a resolution tree.
 * `cycle` and `truncated` are safety stops emitted when the resolver
 * bails out (visited cycle or `max_nodes` hit).
 */
export type NodeType =
  | 'direct'
  | 'implied'
  | 'userset'
  | 'ttu'
  | 'union'
  | 'intersection'
  | 'exclusion'
  | 'wildcard'
  | 'cycle'
  | 'truncated';

/**
 * TupleRef identifies a single row in `melange_tuples` that contributed
 * to the resolution. Returned as evidence under Explain nodes.
 */
export interface TupleRef {
  readonly subject_type: ObjectType;
  readonly subject_id: string;
  readonly relation: Relation;
  readonly object_type: ObjectType;
  readonly object_id: string;
}

/**
 * SubjectRef names a subject appearing on a wildcard sentinel.
 * `type` may include a userset suffix ("group#member"); `id` is "*"
 * on wildcard sentinel nodes.
 */
export interface SubjectRef {
  readonly type: string;
  readonly id: string;
}

/**
 * Node is a single step in the resolution tree.
 *
 * Field population depends on `type` — see NodeType. `evidence` shows
 * the tuples that satisfied an Explain leaf node; `children` carries
 * sub-resolutions; `users` carries a single sentinel entry with id="*"
 * on wildcard nodes.
 */
export interface TraceNode {
  readonly type: NodeType;
  readonly label?: string;
  readonly evidence?: TupleRef[];
  readonly children?: TraceNode[];
  readonly users?: SubjectRef[];
  /**
   * Result records whether the branch succeeded or failed. Required for
   * failure-path tracing (the renderer marks "✗ no editor grant"-style
   * entries on denied subtrees). Absent on safety-stop nodes
   * (cycle / truncated).
   */
  readonly result?: boolean;
}

/**
 * Trace is the root of a resolution tree returned by `Checker.explain`.
 *
 * `truncated`/`node_count` are populated when the underlying generated
 * function hit the configured node-count cap.
 */
export interface Trace {
  readonly object: string;
  readonly relation: Relation;
  readonly subject?: string;
  readonly result?: boolean;
  /**
   * Root node of the resolution tree. Always present in a valid response
   * (the SQL helper makes it required); `null` is reserved for degenerate
   * cases such as an Explain on a relation that does not exist.
   */
  readonly root: TraceNode | null;
  readonly truncated?: boolean;
  readonly node_count?: number;
}

/**
 * ExplainOptions controls a single `Checker.explain` call. Options take
 * precedence over per-session `SET melange.max_explain_nodes`; both take
 * precedence over the server-side default (100).
 */
export interface ExplainOptions {
  /** Cap on total nodes in the trace. Resolves via session GUC when omitted. */
  maxNodes?: number;
}
