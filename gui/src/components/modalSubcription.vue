<template>
  <div class="modal-card" style="max-width: 400px; margin: auto">
    <header class="modal-card-head">
      <p class="modal-card-title">{{ $t("configureSubscription.title") }}</p>
    </header>
    <section class="modal-card-body">
      <b-field label="SUBSCRIPTION">
        <b-input
          v-model="form.address"
          type="textarea"
          :placeholder="$t('subscription.subscription')"
        />
      </b-field>
      <b-field label="REMARKS">
        <b-input
          v-model="form.remarks"
          :placeholder="$t('subscription.remarks')"
        />
      </b-field>
      <b-field label="AUTO-SELECT">
        <b-checkbox v-model="form.autoSelect"
          >{{ $t("subscription.autoSelect") }}
        </b-checkbox>
      </b-field>
    </section>
    <footer class="modal-card-foot flex-end">
      <button class="button" type="button" @click="$parent.close()">
        {{ $t("operations.cancel") }}
      </button>
      <button class="button is-primary" @click="handleClickSubmit">
        {{ $t("operations.saveApply") }}
      </button>
    </footer>
  </div>
</template>

<script>
export default {
  name: "ModalSubscription",
  props: {
    which: {
      type: Object,
      default() {
        return null;
      },
    },
  },
  data() {
    return {
      form: {
        address: "",
        remarks: "",
        autoSelect: false,
      },
    };
  },
  watch: {
    which: {
      immediate: true,
      handler(value) {
        this.form = Object.assign(
          {
            address: "",
            remarks: "",
            autoSelect: false,
          },
          value || {}
        );
      },
    },
  },
  methods: {
    handleClickSubmit() {
      this.$emit("submit", Object.assign({}, this.form));
    },
  },
};
</script>

<style lang="scss">
.is-twitter .is-active a {
  color: #4099ff !important;
}
.readonly {
  pointer-events: none;
}
.same-width-5 li {
  width: 5em;
}
</style>
