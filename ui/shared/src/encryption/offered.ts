/**
 * Reads that tolerate a server which does not offer the encryption routes.
 *
 * A client built before EPIC N, or a partial one a test hands a page, has no
 * `getEncryptionPolicy`. The cards ask through these so such a server reads as
 * "not available" (the same code the engine itself answers a deployment with no
 * configuration file) rather than as a thrown TypeError out of an effect.
 */
import { RetndError } from "@shared/api/contracts";
import type { RetndApi } from "@shared/api/contracts";
import type { EncryptionKey, EncryptionPolicy } from "@shared/types/encryption";

const notOffered = () =>
  new RetndError({ code: "ENCRYPTION_UNAVAILABLE", message: "This server does not offer retained-copy encryption." });

export const readPolicy = (api: RetndApi): Promise<EncryptionPolicy> =>
  typeof api.getEncryptionPolicy === "function" ? api.getEncryptionPolicy() : Promise.reject(notOffered());

export const readKeys = (api: RetndApi): Promise<EncryptionKey[]> =>
  typeof api.listEncryptionKeys === "function" ? api.listEncryptionKeys() : Promise.resolve([]);
