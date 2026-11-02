import { readFileSync } from 'node:fs';
import Ajv2020 from 'ajv/dist/2020.js';
import type { ErrorObject, ValidateFunction } from 'ajv';
import addFormats from 'ajv-formats';
import { parse } from 'yaml';

// Contract check of the mocked backend (TST-03/TST-04, WP-530, F-178): every
// request the browser sends below /api/v1 and every answer of the mock is
// checked against api/openapi.yaml. The path and method must be specified;
// path and query parameters, the request body and the response body must
// match their schemas (OpenAPI 3.1 is JSON Schema 2020-12, including
// `format: uuid`); the response status must be one the operation declares.
// A mock that drifts from the server contract, or a page that calls the API
// in a way the server refuses, is a test failure, not a silent success.

type Json = null | boolean | number | string | Json[] | { [key: string]: Json };
type JsonObject = { [key: string]: Json };

interface Parameter {
  name: string;
  in: 'path' | 'query' | 'header' | 'cookie';
  required?: boolean;
  style?: string;
  explode?: boolean;
  schema?: JsonObject;
}

interface Operation {
  template: string;
  parameters: Parameter[];
  requestBody?: { required?: boolean; content?: Record<string, { schema?: JsonObject }> };
  responses: Record<string, JsonObject>;
}

interface Route {
  template: string;
  pattern: RegExp;
  params: string[];
  operations: Map<string, JsonObject>;
  shared: Parameter[];
}

/** A contract violation; the message names method, path and the schema error. */
export interface Violation {
  /** The request field: a body pointer as a.b, a parameter name, 'body' or 'path'. */
  field: string;
  message: string;
}

/** A request as the contract sees it: the full path with /api/v1. */
export interface ContractRequest {
  method: string;
  path: string;
  query: URLSearchParams;
  /** The parsed JSON body; undefined when the request carries none. */
  body?: unknown;
}

/** A response as the contract sees it; body undefined means no body. */
export interface ContractResponse {
  status: number;
  contentType?: string;
  body?: unknown;
}

const SPEC_URL = new URL('../../../api/openapi.yaml', import.meta.url);
const DOC_ID = 'openapi.json';
const METHODS = ['get', 'put', 'post', 'delete', 'patch', 'head', 'options'];

function isJsonType(type: string): boolean {
  return type === 'application/json' || type.endsWith('+json');
}

/** rewriteRefs points every local $ref at the document's schema ID. */
function rewriteRefs(value: Json): Json {
  if (Array.isArray(value)) {
    return value.map(rewriteRefs);
  }
  if (value && typeof value === 'object') {
    const out: JsonObject = {};
    for (const [key, child] of Object.entries(value)) {
      out[key] =
        key === '$ref' && typeof child === 'string' && child.startsWith('#/')
          ? `${DOC_ID}${child}`
          : rewriteRefs(child);
    }
    return out;
  }
  return value;
}

function pointer(doc: JsonObject, ref: string): JsonObject {
  let node: Json = doc;
  for (const raw of ref.replace(`${DOC_ID}#/`, '').split('/')) {
    node = (node as JsonObject)[raw.replace(/~1/g, '/').replace(/~0/g, '~')] as Json;
    if (node === undefined) {
      throw new Error(`api/openapi.yaml: unresolved reference ${ref}`);
    }
  }
  return node as JsonObject;
}

/** deref follows the $ref of a parameter, request body or response object. */
function deref<T>(doc: JsonObject, value: Json): T {
  let node = value as JsonObject;
  while (node && typeof node.$ref === 'string') {
    node = pointer(doc, node.$ref);
  }
  return node as T;
}

function formatErrors(errors: ErrorObject[] | null | undefined): string {
  return (errors ?? [])
    .map((e) => `${e.instancePath || '/'} ${e.message ?? 'is invalid'}`)
    .filter((line, index, all) => all.indexOf(line) === index)
    .join('; ');
}

export class Contract {
  private readonly doc: JsonObject;
  private readonly routes: Route[] = [];
  private readonly ajv: Ajv2020;
  private readonly queryAjv: Ajv2020;
  private readonly compiled = new Map<JsonObject, ValidateFunction>();
  private readonly compiledQuery = new Map<JsonObject, ValidateFunction>();

  constructor(source: string) {
    this.doc = rewriteRefs(parse(source) as Json) as JsonObject;
    const options = { strict: false, allErrors: true, validateSchema: false } as const;
    this.ajv = new Ajv2020(options);
    // Query and path values arrive as strings; they are coerced to the
    // schema type the way the server's parameter binding reads them.
    this.queryAjv = new Ajv2020({ ...options, coerceTypes: 'array' });
    for (const ajv of [this.ajv, this.queryAjv]) {
      addFormats(ajv);
      ajv.addFormat('int64', { type: 'number', validate: Number.isInteger });
      ajv.addFormat('int32', { type: 'number', validate: Number.isInteger });
      ajv.addFormat('double', { type: 'number', validate: () => true });
      ajv.addFormat('float', { type: 'number', validate: () => true });
      ajv.addFormat('binary', () => true);
      ajv.addSchema({ $id: DOC_ID, components: this.doc.components } as JsonObject);
    }
    for (const [template, item] of Object.entries(
      (this.doc.paths ?? {}) as Record<string, JsonObject>,
    )) {
      const params: string[] = [];
      const source = template
        .split(/(\{[^}]+\})/)
        .map((part) => {
          const name = /^\{([^}]+)\}$/.exec(part)?.[1];
          if (name) {
            params.push(name);
            return '([^/]+)';
          }
          return part.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
        })
        .join('');
      const operations = new Map<string, JsonObject>();
      for (const method of METHODS) {
        if (item[method]) {
          operations.set(method.toUpperCase(), item[method] as JsonObject);
        }
      }
      const shared = ((item.parameters ?? []) as Json[]).map((p) => deref<Parameter>(this.doc, p));
      this.routes.push({
        template,
        pattern: new RegExp(`^${source}$`),
        params,
        operations,
        shared,
      });
    }
    // A literal segment wins over a parameter (/cis/bulk before /cis/{id}).
    this.routes.sort((a, b) => a.params.length - b.params.length);
  }

  static load(): Contract {
    return new Contract(readFileSync(SPEC_URL, 'utf8'));
  }

  private validator(schema: JsonObject, coerce = false): ValidateFunction {
    const cache = coerce ? this.compiledQuery : this.compiled;
    let validate = cache.get(schema);
    if (!validate) {
      validate = (coerce ? this.queryAjv : this.ajv).compile(schema);
      cache.set(schema, validate);
    }
    return validate;
  }

  /** operation finds the specified operation of a request, or says why there is none. */
  operation(
    method: string,
    path: string,
  ): { operation: Operation; pathParams: Record<string, string> } | { error: string } {
    const matching = this.routes.filter((r) => r.pattern.test(path));
    if (matching.length === 0) {
      return { error: `${method} ${path}: route not specified in api/openapi.yaml` };
    }
    for (const route of matching) {
      const op = route.operations.get(method);
      if (!op) {
        continue;
      }
      const values = route.pattern.exec(path)!.slice(1);
      const pathParams = Object.fromEntries(
        route.params.map((name, i) => [name, decodeURIComponent(values[i] ?? '')]),
      );
      const own = ((op.parameters ?? []) as Json[]).map((p) => deref<Parameter>(this.doc, p));
      // Operation parameters override path-level ones of the same name and place.
      const parameters = [
        ...route.shared.filter((s) => !own.some((o) => o.name === s.name && o.in === s.in)),
        ...own,
      ];
      return {
        operation: {
          template: route.template,
          parameters,
          requestBody: op.requestBody
            ? deref<Operation['requestBody']>(this.doc, op.requestBody)
            : undefined,
          responses: (op.responses ?? {}) as Record<string, JsonObject>,
        },
        pathParams,
      };
    }
    const templates = matching.map((r) => r.template).join(', ');
    return {
      error: `${method} ${path}: method not specified for ${templates} in api/openapi.yaml`,
    };
  }

  /** checkRequest returns the contract violations of a request (none when it conforms). */
  checkRequest(request: ContractRequest): Violation[] {
    const found = this.operation(request.method, request.path);
    if ('error' in found) {
      return [{ field: 'path', message: found.error }];
    }
    const { operation, pathParams } = found;
    const where = `${request.method} ${request.path}`;
    const problems: Violation[] = [];
    const add = (field: string, message: string) => problems.push({ field, message });

    for (const param of operation.parameters.filter((p) => p.in === 'path')) {
      const value = pathParams[param.name];
      if (param.schema && value !== undefined) {
        const validate = this.validator(param.schema, true);
        if (!validate(value)) {
          add(
            param.name,
            `${where}: path parameter ${param.name}: ${formatErrors(validate.errors)}`,
          );
        }
      }
    }

    // An operation with query parameters must know every one the page sends
    // (a renamed filter would be ignored by the server). An operation
    // without any reads no query, so a parameter there changes nothing.
    const query = operation.parameters.filter((p) => p.in === 'query');
    for (const key of query.length > 0 ? new Set(request.query.keys()) : []) {
      const declared = query.some((p) =>
        p.style === 'deepObject' ? key.startsWith(`${p.name}[`) : p.name === key,
      );
      if (!declared) {
        add(key, `${where}: query parameter ${key} not specified`);
      }
    }
    for (const param of query) {
      if (param.style === 'deepObject') {
        continue;
      }
      const values = request.query.getAll(param.name);
      if (values.length === 0) {
        if (param.required) {
          add(param.name, `${where}: required query parameter ${param.name} missing`);
        }
        continue;
      }
      if (!param.schema) {
        continue;
      }
      let value: unknown = values[0];
      if (param.schema.type === 'array') {
        value =
          param.explode === false || values.length === 1
            ? values.flatMap((v) => v.split(','))
            : values;
      } else if (values.length > 1) {
        add(param.name, `${where}: query parameter ${param.name} repeated`);
        continue;
      }
      const validate = this.validator(param.schema, true);
      if (!validate(value)) {
        add(
          param.name,
          `${where}: query parameter ${param.name}: ${formatErrors(validate.errors)}`,
        );
      }
    }

    const declaredBody = operation.requestBody;
    if (request.body === undefined) {
      if (declaredBody?.required) {
        add('body', `${where}: required request body missing`);
      }
      return problems;
    }
    if (!declaredBody) {
      add('body', `${where}: request body not specified`);
      return problems;
    }
    const media = declaredBody.content?.['application/json'];
    if (!media) {
      const types = Object.keys(declaredBody.content ?? {}).join(', ');
      add('body', `${where}: JSON request body, specified is ${types}`);
      return problems;
    }
    if (media.schema) {
      const validate = this.validator(media.schema);
      if (!validate(request.body)) {
        const seen = new Set<string>();
        for (const e of validate.errors ?? []) {
          const message = `${where}: request body: ${e.instancePath || '/'} ${e.message ?? 'is invalid'}`;
          if (!seen.has(message)) {
            seen.add(message);
            add(e.instancePath.replace(/^\//, '').replace(/\//g, '.') || 'body', message);
          }
        }
      }
    }
    return problems;
  }

  /** response looks up the declared response of a status (exact, NXX, default). */
  response(method: string, path: string, status: number): JsonObject | undefined {
    const found = this.operation(method, path);
    if ('error' in found) {
      return undefined;
    }
    const { responses } = found.operation;
    const declared =
      responses[String(status)] ?? responses[`${String(status)[0]}XX`] ?? responses.default;
    return declared ? deref<JsonObject>(this.doc, declared) : undefined;
  }

  /** mediaType is the media type a response of this status declares (JSON first). */
  mediaType(method: string, path: string, status: number): string | undefined {
    const content = this.response(method, path, status)?.content as JsonObject | undefined;
    const types = Object.keys(content ?? {});
    return types.find(isJsonType) ?? types[0];
  }

  /** checkResponse returns the contract violations of a (mocked) response. */
  checkResponse(request: { method: string; path: string }, response: ContractResponse): string[] {
    const where = `${request.method} ${request.path} -> ${response.status}`;
    const found = this.operation(request.method, request.path);
    if ('error' in found) {
      return [found.error];
    }
    const declared = this.response(request.method, request.path, response.status);
    if (!declared) {
      const statuses = Object.keys(found.operation.responses).join(', ');
      return [`${where}: status not specified (declared: ${statuses})`];
    }
    const content = declared.content as Record<string, { schema?: JsonObject }> | undefined;
    if (!content) {
      return response.body === undefined ? [] : [`${where}: response body not specified`];
    }
    if (response.body === undefined) {
      return [`${where}: response body missing`];
    }
    const type =
      response.contentType ?? this.mediaType(request.method, request.path, response.status);
    const media = type ? content[type] : undefined;
    if (!type || !media) {
      return [`${where}: content type ${type} not specified (${Object.keys(content).join(', ')})`];
    }
    if (media.schema && isJsonType(type)) {
      const validate = this.validator(media.schema);
      if (!validate(response.body)) {
        return [`${where}: response body: ${formatErrors(validate.errors)}`];
      }
    }
    return [];
  }
}

let loaded: Contract | undefined;

/** contract is api/openapi.yaml, parsed once per worker. */
export function contract(): Contract {
  loaded ??= Contract.load();
  return loaded;
}
