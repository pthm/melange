/**
 * Type-level checks over the generated clients in this directory, which
 * internal/clientgen/typescript writes (`just update-golden`). tsc must
 * accept this file: src/generated-client.test.ts runs it.
 */

import { AccessibleByParentFunctions, AccessibleFunctions, ObjectTypes, Relations, issue } from './schema/index.js';
import type { MelangeObject } from '@pthm/melange';
import type { ObjectType, Relation } from './schema/index.js';
import * as noAccessible from './schema-no-accessible/index.js';

// The maps carry the exact installed names as literal types, truncated
// names included.
const canRead: 'accessible_issue_can_read' = AccessibleFunctions.issue.can_read;
const truncated: 'accessible_pull_re_can_read_security_advisories_and_vu_d0edb52c' =
  AccessibleFunctions.pull_request.can_read_security_advisories_and_vulnerability_reports;
const byRepo: 'accessible_issue_can_read_by_repo' = AccessibleByParentFunctions.issue.can_read.repo;

// Lookups by a type and relation the model defines.
function accessible<T extends keyof typeof AccessibleFunctions>(
  objectType: T,
  relation: keyof (typeof AccessibleFunctions)[T],
): string {
  return AccessibleFunctions[objectType][relation] as string;
}
const viaLookup: string = accessible('repository', 'can_read');

// @ts-expect-error team defines no parent-key function
AccessibleByParentFunctions.team;

// With both families off the maps are empty, and the relation constants
// follow the client's selector.
// @ts-expect-error no accessible functions
noAccessible.AccessibleFunctions.issue;
// @ts-expect-error no parent-key functions
noAccessible.AccessibleByParentFunctions.issue;
// @ts-expect-error owner is not selected
noAccessible.Relations.Owner;
const canReadRel: noAccessible.Relation = noAccessible.Relations.CanRead;

const object: MelangeObject = issue('1');
const objectType: ObjectType = ObjectTypes.Issue;
const relation: Relation = Relations.CanRead;

export { byRepo, canRead, canReadRel, object, objectType, relation, truncated, viaLookup };
