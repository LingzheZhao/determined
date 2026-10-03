import Alert from 'hew/Alert';
import Button from 'hew/Button';
import Form, { FormInstance } from 'hew/Form';
import Input from 'hew/Input';
import InputNumber from 'hew/InputNumber';
import { Modal, useModal } from 'hew/Modal';
import Row from 'hew/Row';
import Select, { Option, SelectValue } from 'hew/Select';
import Spinner from 'hew/Spinner';
import { Loadable, Loaded, NotLoaded } from 'hew/utils/loadable';
import { number, string, undefined as undefinedType, union } from 'io-ts';
import yaml from 'js-yaml';
import { isPlainObject } from 'lodash';
import React, { useCallback, useEffect, useId, useMemo, useState } from 'react';

import Link from 'components/Link';
import StartFromSelect, { StartFrom, templateNameFromStartFrom } from 'components/StartFromSelect';
import useFeature from 'hooks/useFeature';
import usePermissions from 'hooks/usePermissions';
import { SettingsConfig, useSettings } from 'hooks/useSettings';
import TemplateCreateModalComponent from 'pages/Templates/TemplateCreateModal';
import { paths } from 'routes/utils';
import clusterStore from 'stores/cluster';
import workspaceStore from 'stores/workspaces';
import { CommandResponse, CommandTask, CommandType, RawJson, ResourcePool, Workspace } from 'types';
import handleError, { ErrorLevel, ErrorType } from 'utils/error';
import { launchJupyterLab, previewJupyterLab } from 'utils/jupyter';
import {
  configFromForm,
  formFieldsFromConfig,
  NtscLaunchOptions,
  NtscLaunchType,
  sanitizeConfig,
  templateFromConfig,
  templateResources,
} from 'utils/ntscConfig';
import { useObservable } from 'utils/observable';
import { launchShell, previewShell } from 'utils/shell';

const DEFAULT_SLOT_COUNT = 1;
const BASE_FORM_ID = 'ntsc-launch-form';

/** The simple form's values. `source` is the "Start from" picker's value. */
interface LaunchFormValues extends NtscLaunchOptions {
  source?: string;
}

const settingsConfigFor = (storagePath: string): SettingsConfig<NtscLaunchOptions> => ({
  settings: {
    name: {
      defaultValue: '',
      skipUrlEncoding: true,
      storageKey: 'name',
      type: union([string, undefinedType]),
    },
    pool: {
      defaultValue: '',
      skipUrlEncoding: true,
      storageKey: 'pool',
      type: union([string, undefinedType]),
    },
    slots: {
      defaultValue: DEFAULT_SLOT_COUNT,
      skipUrlEncoding: true,
      storageKey: 'slots',
      type: union([number, undefinedType]),
    },
    template: {
      defaultValue: undefined,
      skipUrlEncoding: true,
      storageKey: 'template',
      type: union([string, undefinedType]),
    },
    workspaceId: {
      defaultValue: undefined,
      skipUrlEncoding: true,
      storageKey: 'workspaceId',
      type: union([number, undefinedType]),
    },
  },
  storagePath,
});

interface TypeCopy {
  configError: string;
  configRequired: string;
  docsLabel: string;
  settings: SettingsConfig<NtscLaunchOptions>;
  title: string;
}

/**
 * Per-type text and the per-user settings (last-used form values, kept on the
 * server). JupyterLab keeps its existing 'jupyter-lab' storage path.
 */
const TYPE_COPY: Record<NtscLaunchType, TypeCopy> = {
  [CommandType.JupyterLab]: {
    configError: 'Unable to fetch JupyterLab config.',
    configRequired: 'JupyterLab config required',
    docsLabel: 'Read about JupyterLab settings',
    settings: settingsConfigFor('jupyter-lab'),
    title: 'Launch JupyterLab',
  },
  [CommandType.Shell]: {
    configError: 'Unable to fetch shell config.',
    configRequired: 'Shell config required',
    docsLabel: 'Read about shell settings',
    settings: settingsConfigFor('shell-launch'),
    title: 'Launch Shell',
  },
};

const settingsFromForm = (values: LaunchFormValues): NtscLaunchOptions => ({
  name: values.name,
  pool: values.pool,
  slots: values.slots,
  template: templateNameFromStartFrom(values.source),
  workspaceId: values.workspaceId,
});

export interface Props {
  /** "Launch Again": start from this task's config. */
  initialTask?: CommandTask;
  /** Shells only: called with the new shell after a successful launch. */
  onLaunched?: (response: CommandResponse) => void;
  type: NtscLaunchType;
  workspace?: Workspace;
}

const CodeEditor = React.lazy(() => import('hew/CodeEditor'));

/**
 * The launch modal shared by JupyterLab and shells: a simple form (workspace,
 * "Start from", name, pool, slots) and a full-config YAML mode.
 *
 * Start from a template: the template name is sent with the simple fields.
 * Start from a config (a recent task or this browser's history): the config
 * is the base and the simple fields are applied on top of it; no template is
 * sent.
 */
const NtscLaunchModalComponent: React.FC<Props> = ({
  initialTask,
  onLaunched,
  type,
  workspace,
}: Props) => {
  const copy = TYPE_COPY[type];
  const idPrefix = useId();
  const [showFullConfig, setShowFullConfig] = useState(false);
  const [config, setConfig] = useState<Loadable<string>>(NotLoaded);
  const [configError, setConfigError] = useState<string>();
  const [fullConfigFormInvalid, setFullConfigFormInvalid] = useState(true);
  const [baseConfig, setBaseConfig] = useState<RawJson>();
  const [redactedEnv, setRedactedEnv] = useState<string[]>([]);
  const [templateDraft, setTemplateDraft] = useState<string>();
  const [autoSelectStartFrom, setAutoSelectStartFrom] = useState(true);
  const [form] = Form.useForm<LaunchFormValues>();
  const [fullConfigForm] = Form.useForm();
  const { canCreateTemplateWorkspace, canCreateWorkspaceNSC } = usePermissions();
  const templatesOn = useFeature().isOn('task_templates');
  const TemplateCreateModal = useModal(TemplateCreateModalComponent);
  const openTemplateCreate = TemplateCreateModal.open;
  const workspaces = Loadable.getOrElse([], useObservable(workspaceStore.workspaces)).filter(
    (workspace) => canCreateWorkspaceNSC({ workspace }),
  );
  const [currentWorkspace, setCurrentWorkspace] = useState<Workspace | undefined>(
    workspace ?? workspaces.at(0),
  );
  const preview = type === CommandType.Shell ? previewShell : previewJupyterLab;

  const validateFullConfigForm = useCallback(() => {
    const fields = fullConfigForm.getFieldsError();
    const hasError = fields.some((f) => f.errors.length) || !currentWorkspace;
    setFullConfigFormInvalid(hasError);
  }, [currentWorkspace, fullConfigForm]);

  const { settings: defaults, updateSettings: updateDefaults } = useSettings<NtscLaunchOptions>(
    copy.settings,
  );

  const handleModalClose = useCallback(() => {
    const fields: LaunchFormValues = form.getFieldsValue(true);
    updateDefaults(settingsFromForm(fields));
  }, [form, updateDefaults]);

  /** The launch or preview options for the simple form's current values. */
  const simpleOptions = useCallback(
    (fields: LaunchFormValues) =>
      baseConfig
        ? { config: configFromForm(baseConfig, fields), workspaceId: fields.workspaceId }
        : {
            name: fields.name,
            pool: fields.pool,
            slots: fields.slots,
            template: templateNameFromStartFrom(fields.source),
            workspaceId: fields.workspaceId,
          },
    [baseConfig],
  );

  const fetchConfig = useCallback(async () => {
    setConfig(NotLoaded);

    const fields: LaunchFormValues = form.getFieldsValue(true);
    try {
      const newConfig = await preview(simpleOptions(fields));
      setConfig(Loaded(yaml.dump(newConfig)));
    } catch (e) {
      setConfigError(copy.configError);
    }
  }, [copy.configError, form, preview, simpleOptions]);

  const handleSecondary = useCallback(() => {
    if (showFullConfig) setFullConfigFormInvalid(false);
    setShowFullConfig((show) => !show);
  }, [showFullConfig]);

  const launch = useCallback(
    async (options: NtscLaunchOptions & { config?: RawJson }) => {
      if (type === CommandType.Shell) {
        const response = await launchShell(options);
        onLaunched?.(response);
      } else {
        // JupyterLab opens in a new tab and reports its own errors.
        launchJupyterLab(options);
      }
    },
    [onLaunched, type],
  );

  const handleSubmit = useCallback(async () => {
    const fields: LaunchFormValues = form.getFieldsValue(true);
    updateDefaults(settingsFromForm(fields));
    if (showFullConfig) {
      const values = await fullConfigForm.validateFields();
      const usableConfig = Loadable.isLoaded(config) ? config.data : '';

      if (values) {
        await launch({
          config: yaml.load(usableConfig) as RawJson,
          workspaceId: values.workspaceId,
        });
      }
    } else {
      const values = await form.validateFields();
      if (values) await launch(simpleOptions(fields));
    }
  }, [config, fullConfigForm, form, launch, showFullConfig, simpleOptions, updateDefaults]);

  const handleConfigChange = useCallback(
    (config: string) => {
      validateFullConfigForm();
      setConfig(Loaded(config));
      setConfigError(undefined);
    },
    [validateFullConfigForm],
  );

  const handleStartFromAutoSelected = useCallback(() => setAutoSelectStartFrom(false), []);

  const handleStartFrom = useCallback(
    (start: StartFrom) => {
      if (start.kind === 'config') {
        const base = sanitizeConfig(start.config);
        const fields = formFieldsFromConfig(base);
        setBaseConfig(base);
        setRedactedEnv(start.redactedEnv);
        form.setFieldsValue({ name: fields.name, pool: fields.pool, slots: fields.slots });
        if (!workspace) {
          const sourceWorkspace = workspaces.find((w) => w.id === start.workspaceId);
          if (sourceWorkspace) setCurrentWorkspace(sourceWorkspace);
        }
        return;
      }
      setBaseConfig(undefined);
      setRedactedEnv([]);
      if (start.kind === 'template') {
        // The master ignores a template's resources for shells and notebooks,
        // so they are copied into the form, which always sends them.
        const { pool, slots } = templateResources(start.template.config);
        if (pool !== undefined) form.setFieldValue('pool', pool);
        if (slots !== undefined) form.setFieldValue('slots', slots);
      }
    },
    [form, workspace, workspaces],
  );

  const canSaveTemplate =
    templatesOn &&
    !!currentWorkspace &&
    canCreateTemplateWorkspace({ workspace: { id: currentWorkspace.id } });

  const handleSaveAsTemplate = useCallback(async () => {
    let parsed: unknown;
    try {
      parsed = yaml.load(Loadable.isLoaded(config) ? config.data : '');
    } catch (e) {
      handleError(e, {
        level: ErrorLevel.Error,
        publicSubject: 'Fix the YAML before saving it as a template.',
        silent: false,
        type: ErrorType.Input,
      });
      return;
    }
    if (!isPlainObject(parsed)) {
      handleError(new Error('The config is empty.'), {
        level: ErrorLevel.Error,
        publicSubject: 'Fix the YAML before saving it as a template.',
        silent: false,
        type: ErrorType.Input,
      });
      return;
    }
    const parsedConfig = parsed as RawJson;
    // The cluster defaults for the same workspace, pool and slots, so that the
    // template only keeps what differs from them.
    let defaultsConfig: RawJson | undefined;
    try {
      const resources = templateResources(parsedConfig);
      defaultsConfig = await preview({
        pool: resources.pool,
        slots: resources.slots,
        workspaceId: currentWorkspace?.id,
      });
    } catch {
      defaultsConfig = undefined;
    }
    const header = defaultsConfig
      ? '# Only the settings that differ from the cluster defaults are kept.\n'
      : '';
    setTemplateDraft(header + yaml.dump(templateFromConfig(parsedConfig, defaultsConfig)));
    openTemplateCreate();
  }, [config, currentWorkspace?.id, openTemplateCreate, preview]);

  useEffect(validateFullConfigForm, [currentWorkspace, validateFullConfigForm]);

  useEffect(() => workspaceStore.fetch(), []);

  // Fetch full config when showing advanced mode.
  useEffect(() => {
    if (showFullConfig) {
      fetchConfig();
    }
  }, [fetchConfig, showFullConfig]);

  return (
    <Modal
      cancel
      footerLink={
        showFullConfig ? (
          <Link
            external
            path={paths.docs('/architecture/introduction.html#interactive-job-configuration')}
            popout>
            {copy.docsLabel}
          </Link>
        ) : undefined
      }
      size={showFullConfig ? 'large' : 'small'}
      submit={{
        disabled: showFullConfig ? fullConfigFormInvalid : !currentWorkspace?.id,
        form: idPrefix + (showFullConfig ? '-full-' : '-simple-') + BASE_FORM_ID,
        handleError,
        handler: handleSubmit,
        text: 'Launch',
      }}
      title={copy.title}
      onClose={handleModalClose}>
      {showFullConfig ? (
        <FullConfig
          config={config}
          configError={configError}
          configRequired={copy.configRequired}
          currentWorkspace={currentWorkspace}
          form={fullConfigForm}
          formId={idPrefix + '-full-' + BASE_FORM_ID}
          lockedWorkspace={!!workspace}
          note={
            type === CommandType.Shell
              ? 'Shells have no preview of their own: this config is previewed like a JupyterLab, with the JupyterLab-only settings (idle_timeout, notebook_idle_type) removed. The master checks it again at launch.'
              : undefined
          }
          setWorkspace={setCurrentWorkspace}
          workspaces={workspaces}
          onChange={handleConfigChange}
        />
      ) : (
        <LaunchForm
          autoSelectStartFrom={autoSelectStartFrom}
          currentWorkspace={currentWorkspace}
          defaults={defaults}
          form={form}
          formId={idPrefix + '-simple-' + BASE_FORM_ID}
          initialTask={initialTask}
          lockedWorkspace={!!workspace}
          setWorkspace={setCurrentWorkspace}
          type={type}
          workspaces={workspaces}
          onStartFrom={handleStartFrom}
          onStartFromAutoSelected={handleStartFromAutoSelected}
        />
      )}
      {redactedEnv.length > 0 && (
        <Alert
          description={`Add them back in the full config if this task needs them: ${redactedEnv.join(', ')}.`}
          message="Environment variables that looked like credentials were not saved in this browser."
          showIcon
          type="warning"
        />
      )}
      <Row>
        <Button onClick={handleSecondary}>
          {showFullConfig ? 'Show Simple Config' : 'Show Full Config'}
        </Button>
        {showFullConfig && canSaveTemplate && (
          <Button disabled={!Loadable.isLoaded(config)} onClick={handleSaveAsTemplate}>
            Save as Template
          </Button>
        )}
      </Row>
      {templateDraft !== undefined && (
        <TemplateCreateModal.Component
          initialConfig={templateDraft}
          initialWorkspaceId={currentWorkspace?.id}
        />
      )}
    </Modal>
  );
};

export default NtscLaunchModalComponent;

interface FullConfigProps {
  config: Loadable<string>;
  configError?: string;
  configRequired: string;
  currentWorkspace?: Workspace;
  form: FormInstance;
  formId: string;
  lockedWorkspace: boolean;
  note?: string;
  onChange?: (config: string) => void;
  setWorkspace: (arg0: Workspace | undefined) => void;
  workspaces: Workspace[];
}

const FullConfig: React.FC<FullConfigProps> = ({
  config,
  configError,
  configRequired,
  currentWorkspace,
  form,
  formId,
  lockedWorkspace,
  note,
  onChange,
  setWorkspace,
  workspaces,
}: FullConfigProps) => {
  const usableConfig = useMemo(() => (Loadable.isLoaded(config) ? config.data : ''), [config]);
  const [field, setField] = useState([
    { name: 'config', value: usableConfig },
    { name: 'workspaceId', value: currentWorkspace ? currentWorkspace.id : workspaces.at(0)?.id },
  ]);

  const handleConfigChange = useCallback(
    (_: unknown, allFields: unknown) => {
      if (!Array.isArray(allFields) || allFields.length === 0) return;
      try {
        const configString = allFields.find((field) => field.name[0] === 'config').value;
        onChange?.(configString);
      } catch (e) {
        handleError(e);
      }
    },
    [onChange],
  );

  useEffect(() => {
    setField((curField) => [
      ...curField.filter((f) => f.name[0] === 'workspaceId'),
      { name: 'config', value: usableConfig },
    ]);
  }, [usableConfig]);

  useEffect(() => {
    form.setFieldValue(
      'workspaceId',
      currentWorkspace ? currentWorkspace.id : workspaces.at(0)?.id,
    );
  }, [currentWorkspace, form, workspaces]);
  useEffect(() => {
    form.setFieldValue('config', usableConfig);
  }, [usableConfig, form]);

  const onSelectWorkspace = (workspaceId?: SelectValue) => {
    const selected = workspaces.find((w) => workspaceId && w.id === workspaceId);
    setWorkspace(selected);
  };

  return (
    <Form fields={field} form={form} id={formId} onFieldsChange={handleConfigChange}>
      <React.Suspense fallback={<Spinner spinning tip="Loading text editor..." />}>
        <Form.Item
          initialValue={currentWorkspace?.id}
          label="Workspace"
          name="workspaceId"
          rules={[{ message: 'Workspace is required', required: true, type: 'number' }]}>
          <Select
            allowClear
            disabled={lockedWorkspace}
            placeholder="Workspace (required)"
            onChange={onSelectWorkspace}>
            {workspaces.map((workspace: Workspace) => (
              <Option key={workspace.id} value={workspace.id}>
                {workspace.name}
              </Option>
            ))}
          </Select>
        </Form.Item>
        <Form.Item
          extra={note}
          initialValue={usableConfig}
          name="config"
          rules={[
            { message: configRequired, required: true },
            {
              validator: (_rule, value) => {
                try {
                  yaml.load(value);
                  return Promise.resolve();
                } catch (err: unknown) {
                  return Promise.reject(
                    new Error(
                      `Invalid YAML on line ${(err as { mark: { line: string } }).mark.line}.`,
                    ),
                  );
                }
              },
            },
          ]}>
          <CodeEditor
            file={config}
            files={[{ key: 'config.yaml' }]}
            height="40vh"
            onError={handleError}
          />
        </Form.Item>
      </React.Suspense>
      {configError && <Alert message={configError} type="error" />}
    </Form>
  );
};

interface LaunchFormProps {
  autoSelectStartFrom: boolean;
  currentWorkspace?: Workspace;
  defaults: NtscLaunchOptions;
  form: FormInstance<LaunchFormValues>;
  formId: string;
  initialTask?: CommandTask;
  lockedWorkspace: boolean;
  onStartFrom: (start: StartFrom) => void;
  onStartFromAutoSelected: () => void;
  setWorkspace: (arg0: Workspace | undefined) => void;
  type: NtscLaunchType;
  workspaces: Workspace[];
}

const LaunchForm: React.FC<LaunchFormProps> = ({
  autoSelectStartFrom,
  currentWorkspace,
  defaults,
  form,
  formId,
  initialTask,
  lockedWorkspace,
  onStartFrom,
  onStartFromAutoSelected,
  setWorkspace,
  type,
  workspaces,
}: LaunchFormProps) => {
  const selectedWorkspaceId = Form.useWatch('workspaceId', form);
  const templatesOn = useFeature().isOn('task_templates');

  const resourcePools = useObservable(clusterStore.resourcePools);
  const boundResourcePoolsMap = useObservable(workspaceStore.boundResourcePoolsMap());

  const boundResourcePools: ResourcePool[] = useMemo(() => {
    if (!Loadable.isLoaded(resourcePools) || !selectedWorkspaceId) return [];
    return resourcePools.data.filter((rp) =>
      boundResourcePoolsMap.get(selectedWorkspaceId)?.includes(rp.name),
    );
  }, [resourcePools, boundResourcePoolsMap, selectedWorkspaceId]);

  const selectedPoolName = Form.useWatch('pool', form);

  const resourceInfo = useMemo(() => {
    const selectedPool = boundResourcePools.find((pool) => pool.name === selectedPoolName);
    if (!selectedPool) return { hasAux: false, hasCompute: false, maxSlots: 0 };

    /**
     * For static resource pools, the slots-per-agent comes through as -1,
     * meaning it is unknown how many we may have.
     */
    const hasAuxCapacity = selectedPool.auxContainerCapacityPerAgent > 0;
    const hasSlots = selectedPool.slotsAvailable > 0;
    const maxSlots = selectedPool.slotsPerAgent ?? 0;
    const hasSlotsPerAgent = maxSlots !== 0;
    const hasComputeCapacity = hasSlots || hasSlotsPerAgent;

    return {
      hasAux: hasAuxCapacity,
      hasCompute: hasComputeCapacity,
      maxSlots: maxSlots,
    };
  }, [selectedPoolName, boundResourcePools]);

  const allowedWorkspaceIds = useMemo(() => workspaces.map((w) => w.id), [workspaces]);

  useEffect(() => {
    if (!resourceInfo.hasCompute && resourceInfo.hasAux) form.setFieldValue('slots', 0);
    else if (resourceInfo.hasCompute) {
      const slots = form.getFieldValue('slots');
      if (slots == null) form.setFieldValue('slots', DEFAULT_SLOT_COUNT);
    }
  }, [resourceInfo, form]);

  useEffect(() => {
    selectedWorkspaceId && workspaceStore.fetchAvailableResourcePools(selectedWorkspaceId);
  }, [selectedWorkspaceId]);

  useEffect(() => {
    const fields = form.getFieldsValue(true);
    if (!fields?.pool && boundResourcePools[0]?.name) {
      const firstPoolInList = boundResourcePools[0]?.name;
      form.setFieldValue('pool', firstPoolInList);
    }
  }, [boundResourcePools, form]);

  useEffect(() => {
    form.setFieldValue(
      'workspaceId',
      currentWorkspace ? currentWorkspace.id : workspaces.at(0)?.id,
    );
  }, [currentWorkspace, form, workspaces]);

  const onSelectWorkspace = (workspaceId?: SelectValue) => {
    const selected = workspaces.find((w) => workspaceId && w.id === workspaceId);
    setWorkspace(selected);
  };

  return (
    <Form form={form} id={formId}>
      <Form.Item
        initialValue={currentWorkspace?.id}
        label="Workspace"
        name="workspaceId"
        rules={[{ message: 'Workspace is required', required: true, type: 'number' }]}>
        <Select
          allowClear
          disabled={lockedWorkspace}
          placeholder="Workspace (required)"
          onChange={onSelectWorkspace}>
          {workspaces.map((workspace: Workspace) => (
            <Option key={workspace.id} value={workspace.id}>
              {workspace.name}
            </Option>
          ))}
        </Select>
      </Form.Item>
      <Form.Item
        extra={templatesOn && <Link path={paths.templates()}>Manage Templates</Link>}
        label="Start from"
        name="source">
        <StartFromSelect
          allowedWorkspaceIds={allowedWorkspaceIds}
          autoSelect={autoSelectStartFrom}
          defaultTemplate={defaults.template}
          initialTask={initialTask}
          lockedWorkspaceId={lockedWorkspace ? currentWorkspace?.id : undefined}
          type={type}
          onAutoSelected={onStartFromAutoSelected}
          onResolve={onStartFrom}
        />
      </Form.Item>
      <Form.Item initialValue={defaults?.name} label="Name" name="name">
        <Input placeholder="Name (optional)" />
      </Form.Item>
      <Form.Item label="Resource Pool" name="pool">
        <Select allowClear disabled={!selectedWorkspaceId} placeholder="Pick the best option">
          {boundResourcePools.map((pool) => (
            <Option key={pool.name} value={pool.name}>
              {pool.name}
            </Option>
          ))}
        </Select>
      </Form.Item>
      <Form.Item
        hidden={!resourceInfo.hasCompute}
        initialValue={defaults?.slots}
        label="Slots"
        name="slots">
        <InputNumber
          max={resourceInfo.maxSlots === -1 ? Number.MAX_SAFE_INTEGER : resourceInfo.maxSlots}
          min={0}
        />
      </Form.Item>
    </Form>
  );
};
