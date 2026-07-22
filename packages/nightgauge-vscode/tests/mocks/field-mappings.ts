/**
 * Mock fixtures for project field mappings
 *
 * Provides test data for project field mappings (from config.yaml project.fields).
 * Used by tests that need to verify field option ID lookups.
 *
 * @module tests/mocks/field-mappings
 */

/**
 * Structure of the project field mappings configuration file
 */
export interface FieldMappingsConfig {
  $schema?: string;
  version: string;
  description?: string;
  project: {
    id: string;
  };
  fields: {
    status: {
      id: string;
      options: Record<string, string>;
    };
    priority: {
      id: string;
      options: Record<string, string>;
    };
    size: {
      id: string;
      options: Record<string, string>;
    };
  };
}

/**
 * Mock field mappings configuration matching the structure of
 * config.yaml project.fields (title-case format used at runtime)
 *
 * Uses test-specific option IDs to avoid confusion with real values.
 */
export const MOCK_FIELD_MAPPINGS: FieldMappingsConfig = {
  $schema: "https://nightgauge.dev/schemas/project-field-mappings.json",
  version: "1.0",
  description: "Test field mappings for unit tests",
  project: {
    id: "PVT_test_project_id",
  },
  fields: {
    status: {
      id: "PVTSSF_test_status",
      options: {
        Backlog: "opt_backlog_id",
        Ready: "opt_ready_id",
        "In progress": "opt_in_progress_id",
        "In review": "opt_in_review_id",
        Done: "opt_done_id",
      },
    },
    priority: {
      id: "PVTSSF_test_priority",
      options: {
        P0: "opt_p0_id",
        P1: "opt_p1_id",
        P2: "opt_p2_id",
        P3: "opt_p3_id",
      },
    },
    size: {
      id: "PVTSSF_test_size",
      options: {
        XS: "opt_xs_id",
        S: "opt_s_id",
        M: "opt_m_id",
        L: "opt_l_id",
        XL: "opt_xl_id",
      },
    },
  },
};

/**
 * Create a mock field mappings config with optional overrides
 *
 * @param overrides - Partial overrides for the config
 * @returns Complete field mappings configuration
 */
export function createMockFieldMappings(
  overrides?: Partial<FieldMappingsConfig>
): FieldMappingsConfig {
  return {
    ...MOCK_FIELD_MAPPINGS,
    ...overrides,
  };
}

/**
 * Create field mappings with custom status options
 *
 * @param statusOptions - Custom status options mapping
 * @returns Field mappings with modified status field
 */
export function createMockFieldMappingsWithStatusOptions(
  statusOptions: Record<string, string>
): FieldMappingsConfig {
  return {
    ...MOCK_FIELD_MAPPINGS,
    fields: {
      ...MOCK_FIELD_MAPPINGS.fields,
      status: {
        ...MOCK_FIELD_MAPPINGS.fields.status,
        options: statusOptions,
      },
    },
  };
}

/**
 * Create field mappings with custom priority options
 *
 * @param priorityOptions - Custom priority options mapping
 * @returns Field mappings with modified priority field
 */
export function createMockFieldMappingsWithPriorityOptions(
  priorityOptions: Record<string, string>
): FieldMappingsConfig {
  return {
    ...MOCK_FIELD_MAPPINGS,
    fields: {
      ...MOCK_FIELD_MAPPINGS.fields,
      priority: {
        ...MOCK_FIELD_MAPPINGS.fields.priority,
        options: priorityOptions,
      },
    },
  };
}

/**
 * Create field mappings with custom size options
 *
 * @param sizeOptions - Custom size options mapping
 * @returns Field mappings with modified size field
 */
export function createMockFieldMappingsWithSizeOptions(
  sizeOptions: Record<string, string>
): FieldMappingsConfig {
  return {
    ...MOCK_FIELD_MAPPINGS,
    fields: {
      ...MOCK_FIELD_MAPPINGS.fields,
      size: {
        ...MOCK_FIELD_MAPPINGS.fields.size,
        options: sizeOptions,
      },
    },
  };
}

/**
 * Create empty field mappings (no options configured)
 *
 * Useful for testing edge cases where field mappings are missing.
 */
export const MOCK_EMPTY_FIELD_MAPPINGS: FieldMappingsConfig = {
  version: "1.0",
  project: {
    id: "PVT_test_empty",
  },
  fields: {
    status: {
      id: "PVTSSF_test_status_empty",
      options: {},
    },
    priority: {
      id: "PVTSSF_test_priority_empty",
      options: {},
    },
    size: {
      id: "PVTSSF_test_size_empty",
      options: {},
    },
  },
};

/**
 * Field mappings with partial options (some values missing)
 *
 * Useful for testing graceful handling of incomplete configurations.
 */
export const MOCK_PARTIAL_FIELD_MAPPINGS: FieldMappingsConfig = {
  version: "1.0",
  project: {
    id: "PVT_test_partial",
  },
  fields: {
    status: {
      id: "PVTSSF_test_status_partial",
      options: {
        Ready: "opt_ready_id",
        "In progress": "opt_in_progress_id",
        // Missing: Backlog, In review, Done
      },
    },
    priority: {
      id: "PVTSSF_test_priority_partial",
      options: {
        P0: "opt_p0_id",
        // Missing: P1, P2
      },
    },
    size: {
      id: "PVTSSF_test_size_partial",
      options: {
        S: "opt_s_id",
        M: "opt_m_id",
        // Missing: XS, L, XL
      },
    },
  },
};

// ============================================================================
// Helper Functions for Testing Option ID Lookups
// ============================================================================

/**
 * Get a status option ID from the mock mappings
 *
 * @param statusValue - The status field value (e.g., 'In progress')
 * @returns The option ID or undefined if not found
 */
export function getMockStatusOptionId(statusValue: string): string | undefined {
  return MOCK_FIELD_MAPPINGS.fields.status.options[statusValue];
}

/**
 * Get a priority option ID from the mock mappings
 *
 * @param priorityValue - The priority field value (e.g., 'P1')
 * @returns The option ID or undefined if not found
 */
export function getMockPriorityOptionId(priorityValue: string): string | undefined {
  return MOCK_FIELD_MAPPINGS.fields.priority.options[priorityValue];
}

/**
 * Get a size option ID from the mock mappings
 *
 * @param sizeValue - The size field value (e.g., 'M')
 * @returns The option ID or undefined if not found
 */
export function getMockSizeOptionId(sizeValue: string): string | undefined {
  return MOCK_FIELD_MAPPINGS.fields.size.options[sizeValue];
}

// ============================================================================
// Reverse Lookup Helpers
// ============================================================================

/**
 * Get status name from option ID (reverse lookup)
 *
 * @param optionId - The option ID
 * @returns The status name or undefined if not found
 */
export function getMockStatusNameFromOptionId(optionId: string): string | undefined {
  const options = MOCK_FIELD_MAPPINGS.fields.status.options;
  return Object.entries(options).find(([, id]) => id === optionId)?.[0];
}

/**
 * Get priority name from option ID (reverse lookup)
 *
 * @param optionId - The option ID
 * @returns The priority name or undefined if not found
 */
export function getMockPriorityNameFromOptionId(optionId: string): string | undefined {
  const options = MOCK_FIELD_MAPPINGS.fields.priority.options;
  return Object.entries(options).find(([, id]) => id === optionId)?.[0];
}

/**
 * Get size name from option ID (reverse lookup)
 *
 * @param optionId - The option ID
 * @returns The size name or undefined if not found
 */
export function getMockSizeNameFromOptionId(optionId: string): string | undefined {
  const options = MOCK_FIELD_MAPPINGS.fields.size.options;
  return Object.entries(options).find(([, id]) => id === optionId)?.[0];
}
