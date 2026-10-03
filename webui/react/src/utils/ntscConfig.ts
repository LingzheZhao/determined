import { cloneDeep, isEqual, isPlainObject } from 'lodash';

import { CommandType, RawJson } from 'types';

/** The task types the launch modal can start. */
export type NtscLaunchType = typeof CommandType.JupyterLab | typeof CommandType.Shell;

/** The simple launch form's fields, as the launch helpers take them. */
export interface NtscLaunchOptions {
  name?: string;
  pool?: string;
  slots?: number;
  template?: string;
  workspaceId?: number;
}

export interface LaunchFormFields {
  name?: string;
  pool?: string;
  slots?: number;
}

/**
 * The master names a shell or notebook "Shell (<pet name>)" or
 * "JupyterLab (<pet name>)" when no description is given.
 */
const AUTO_DESCRIPTION = /^(JupyterLab|Shell) \([a-z]+(-[a-z]+)*\)$/;
const AUTO_JUPYTERLAB_DESCRIPTION = /^JupyterLab \([a-z]+(-[a-z]+)*\)$/;

/** Environment variable names that probably hold a credential. */
const SECRET_ENV_NAME = /TOKEN|SECRET|PASSW|KEY|AUTH|CRED/i;

const asObject = (value: unknown): RawJson | undefined =>
  isPlainObject(value) ? (value as RawJson) : undefined;

export const isAutoDescription = (description: unknown): boolean =>
  typeof description === 'string' && AUTO_DESCRIPTION.test(description);

/** The config the simple form sends: a description and the resources. */
export const simpleLaunchConfig = (options: NtscLaunchOptions): RawJson => ({
  description: options.name === '' ? undefined : options.name,
  resources: {
    resource_pool: options.pool === '' ? undefined : options.pool,
    slots: options.slots,
  },
});

/**
 * Prepares a merged config (from the master) for another launch.
 * - entrypoint: the master sets it on every launch (for a shell it holds the
 *   sshd command line with a random port).
 * - an auto-generated description, so the next launch gets a new pet name.
 * - environment.registry_auth: it can hold registry credentials.
 * - resources.priority: the master filled in the pool's default; dropping it
 *   lets the default of the chosen pool apply again.
 * Keys are deleted rather than set to null, so the master's defaults apply.
 * Everything else (image, bind mounts, environment variables, ...) is kept.
 */
export const sanitizeConfig = (config: RawJson): RawJson => {
  const out = cloneDeep(config);
  delete out.entrypoint;
  if (isAutoDescription(out.description)) delete out.description;
  const environment = asObject(out.environment);
  if (environment) delete environment.registry_auth;
  const resources = asObject(out.resources);
  if (resources) delete resources.priority;
  return out;
};

/**
 * Turns a config previewed through the JupyterLab launch API into a shell
 * config.
 *
 * LaunchShell has no preview mode. The master builds the config of every
 * notebook, shell and command the same way (getCommandLaunchParams: cluster
 * defaults, then the template, then the request config, then pool, slots and
 * priority). The notebook preview adds only two notebook-only defaults before
 * it returns: idle_timeout (from notebook_timeout) and the
 * "JupyterLab (<pet name>)" description. A shell never watches idle_timeout,
 * and notebook_idle_type only applies to notebooks, so both are removed. The
 * entrypoint is always replaced by the shell's sshd entrypoint at launch.
 */
export const toShellConfig = (previewConfig: RawJson): RawJson => {
  const out = cloneDeep(previewConfig);
  delete out.idle_timeout;
  delete out.notebook_idle_type;
  delete out.entrypoint;
  if (typeof out.description === 'string' && AUTO_JUPYTERLAB_DESCRIPTION.test(out.description)) {
    delete out.description;
  }
  return out;
};

const dropNulls = (value: RawJson): RawJson => {
  const out: RawJson = {};
  Object.entries(value).forEach(([key, item]) => {
    if (item === null || item === undefined) return;
    const object = asObject(item);
    out[key] = object ? dropNulls(object) : item;
  });
  return out;
};

/**
 * Keeps only the keys of `config` whose values differ from `defaults`.
 * Objects are compared key by key, everything else (including arrays) as a whole.
 */
export const minimalDiff = (config: RawJson, defaults: RawJson): RawJson => {
  const out: RawJson = {};
  Object.entries(config).forEach(([key, value]) => {
    const base = defaults[key];
    if (isEqual(value, base)) return;
    const valueObject = asObject(value);
    const baseObject = asObject(base);
    if (valueObject && baseObject) {
      const diff = minimalDiff(valueObject, baseObject);
      if (Object.keys(diff).length !== 0) out[key] = diff;
      return;
    }
    out[key] = value;
  });
  return out;
};

/**
 * Builds a template from a launch config.
 *
 * It applies sanitizeConfig, always drops the description (a template should
 * not name every task launched from it) and null values (a null in a template
 * overrides the cluster default). When the cluster defaults for the same
 * workspace, pool and slots are known, only the settings that differ from them
 * are kept, so the template keeps following later changes to the defaults.
 * The resource pool and slots are always kept: the master ignores template
 * resources for notebooks and shells, and the launch form copies them into its
 * fields when the template is picked.
 */
export const templateFromConfig = (config: RawJson, defaults?: RawJson): RawJson => {
  const clean = sanitizeConfig(config);
  delete clean.description;
  const full = dropNulls(clean);
  if (!defaults) return full;

  const cleanDefaults = sanitizeConfig(defaults);
  delete cleanDefaults.description;
  const out = minimalDiff(full, dropNulls(cleanDefaults));
  const resources = asObject(full.resources);
  if (resources) {
    const kept: RawJson = {};
    if (resources.resource_pool !== undefined) kept.resource_pool = resources.resource_pool;
    if (resources.slots !== undefined) kept.slots = resources.slots;
    if (Object.keys(kept).length !== 0) out.resources = { ...asObject(out.resources), ...kept };
  }
  return out;
};

/** Reads the simple form's fields out of a config. */
export const formFieldsFromConfig = (config: RawJson): LaunchFormFields => {
  const resources = asObject(config.resources);
  const description = config.description;
  return {
    name: typeof description === 'string' && !isAutoDescription(description) ? description : '',
    pool: typeof resources?.resource_pool === 'string' ? resources.resource_pool : undefined,
    slots: typeof resources?.slots === 'number' ? resources.slots : undefined,
  };
};

/** Applies the simple form's fields on top of a base config. */
export const configFromForm = (base: RawJson, fields: LaunchFormFields): RawJson => {
  const out = cloneDeep(base);
  if (fields.name) out.description = fields.name;
  else delete out.description;

  const resources: RawJson = { ...asObject(out.resources) };
  if (fields.pool) resources.resource_pool = fields.pool;
  else delete resources.resource_pool;
  if (fields.slots !== undefined && fields.slots !== null) resources.slots = fields.slots;
  else delete resources.slots;
  out.resources = resources;
  return out;
};

/** Pool and slots a template sets, if any. */
export const templateResources = (config?: RawJson): LaunchFormFields => {
  const resources = asObject(config?.resources);
  return {
    pool: typeof resources?.resource_pool === 'string' ? resources.resource_pool : undefined,
    slots: typeof resources?.slots === 'number' ? resources.slots : undefined,
  };
};

const ENV_RUNTIMES = ['cpu', 'cuda', 'rocm', 'gpu'];

const envVarName = (entry: unknown): string | undefined =>
  typeof entry === 'string' ? entry.split('=')[0] : undefined;

/**
 * Lists the names of environment variables that look like credentials.
 * environment_variables is either a list of NAME=VALUE strings or a map of
 * such lists per runtime (cpu, cuda, rocm).
 */
export const sensitiveEnvNames = (config: RawJson): string[] => {
  const variables = asObject(config.environment)?.environment_variables;
  const lists: unknown[][] = Array.isArray(variables)
    ? [variables]
    : ENV_RUNTIMES.map((runtime) => asObject(variables)?.[runtime]).filter(Array.isArray);
  const names = new Set<string>();
  lists.flat().forEach((entry) => {
    const name = envVarName(entry);
    if (name && SECRET_ENV_NAME.test(name)) names.add(name);
  });
  return [...names].sort();
};

/**
 * Removes environment variables whose names look like credentials and returns
 * the removed names.
 */
export const redactSensitiveEnv = (config: RawJson): { config: RawJson; redacted: string[] } => {
  const redacted = sensitiveEnvNames(config);
  if (redacted.length === 0) return { config, redacted };

  const out = cloneDeep(config);
  const environment = asObject(out.environment);
  if (!environment) return { config: out, redacted };
  const keep = (list: unknown[]) =>
    list.filter((entry) => {
      const name = envVarName(entry);
      return !name || !SECRET_ENV_NAME.test(name);
    });
  const variables = environment.environment_variables;
  if (Array.isArray(variables)) {
    environment.environment_variables = keep(variables);
  } else {
    const perRuntime = asObject(variables);
    if (perRuntime) {
      ENV_RUNTIMES.forEach((runtime) => {
        if (Array.isArray(perRuntime[runtime])) perRuntime[runtime] = keep(perRuntime[runtime]);
      });
    }
  }
  return { config: out, redacted };
};

/** JSON with object keys sorted, so equal configs give equal strings. */
export const stableStringify = (value: unknown): string => {
  if (Array.isArray(value)) return `[${value.map(stableStringify).join(',')}]`;
  const object = asObject(value);
  if (object) {
    const entries = Object.keys(object)
      .sort()
      .filter((key) => object[key] !== undefined)
      .map((key) => `${JSON.stringify(key)}:${stableStringify(object[key])}`);
    return `{${entries.join(',')}}`;
  }
  return JSON.stringify(value) ?? 'null';
};
